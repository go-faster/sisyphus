package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/gregjones/httpcache/diskcache"
	"github.com/peterbourgon/diskv"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	chunkcode "github.com/go-faster/sisyphus/internal/chunk/code"
	chunkgit "github.com/go-faster/sisyphus/internal/chunk/git"
	chunkmd "github.com/go-faster/sisyphus/internal/chunk/markdown"
	chunkyaml "github.com/go-faster/sisyphus/internal/chunk/yaml"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/ent/syncstate"
	"github.com/go-faster/sisyphus/internal/index"
	filesingest "github.com/go-faster/sisyphus/internal/ingest/files"
	gitingest "github.com/go-faster/sisyphus/internal/ingest/git"
	gitlabingest "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/sisyphus/internal/ingest/jira"
	telegramingest "github.com/go-faster/sisyphus/internal/ingest/telegram"
	"github.com/go-faster/sisyphus/internal/netclient"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/telemetry"

	"github.com/gotd/log/logzap"
	gotdtelegram "github.com/gotd/td/telegram"
)

var (
	errNotConfigured = errors.New("source not configured")
	errLimitReached  = errors.New("limit reached")
)

// indexConcurrency bounds how many documents pipeline.Index runs at once.
// Each call is dominated by an embedding HTTP round-trip, so running several
// in parallel meaningfully speeds up ingestion without overwhelming the
// embedder or Postgres.
const indexConcurrency = 8

func authenticatedHTTPCache(service string, authParts ...string) (*diskcache.Cache, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, errors.Wrap(err, "get user cache dir")
	}

	h := sha256.New()
	for _, part := range authParts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	namespace := hex.EncodeToString(h.Sum(nil))
	cachePath := filepath.Join(cacheDir, "sisyphus", "httpcache", service, namespace)
	if err := os.MkdirAll(cachePath, 0o700); err != nil {
		return nil, errors.Wrap(err, "create cache dir")
	}
	if err := os.Chmod(cachePath, 0o700); err != nil { //nolint:gosec // directory needs execute to traverse
		return nil, errors.Wrap(err, "chmod cache dir")
	}

	return diskcache.NewWithDiskv(diskv.New(diskv.Options{
		BasePath:     cachePath,
		CacheSizeMax: 100 * 1024 * 1024,
		PathPerm:     0o700,
		FilePerm:     0o600,
	})), nil
}

// indexBatch runs p.Index over docs with bounded concurrency. A single
// document's failure is logged and does not stop the others, matching the
// sequential loops it replaces; anyErr reports whether at least one failed.
func indexBatch(ctx context.Context, lg *zap.Logger, p *pipeline.Pipeline, docs []index.Document, dry bool, label string) (processed int, anyErr bool) {
	if dry {
		for _, d := range docs {
			lg.Info("dry-run would index "+label,
				zap.String("source_id", d.SourceID),
				zap.String("title", d.Title))
		}
		return len(docs), false
	}

	var (
		mu       sync.Mutex
		procN    int
		errFound bool
	)
	g := new(errgroup.Group)
	g.SetLimit(indexConcurrency)
	for _, d := range docs {
		g.Go(func() error {
			if ierr := p.Index(ctx, d); ierr != nil {
				lg.Error("index "+label+" failed", zap.Error(ierr), zap.String("source_id", d.SourceID))
				mu.Lock()
				errFound = true
				mu.Unlock()
				return nil
			}
			mu.Lock()
			procN++
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return procN, errFound
}

type runner struct {
	db       *ent.Client
	vectors  pipeline.VectorStore
	cfg      config.Config
	tp       trace.TracerProvider
	mp       metric.MeterProvider
	embedder index.Embedder
}

func (r *runner) runGit(ctx context.Context, reset bool, limit int, dry, prune bool) error {
	lg := zctx.From(ctx).Named("git")
	cfg := r.cfg
	db := r.db
	vectors := r.vectors

	sources := gitSources(cfg.Git.Repos)
	if len(sources) == 0 {
		lg.Info("git not configured, zero sources")
		return errNotConfigured
	}

	// Build pipelines for each content type
	docsPipe, err := r.buildPipeline(r.buildDocChunker())
	if err != nil {
		return errors.Wrap(err, "build docs pipeline")
	}
	manifestPipe, err := r.buildPipeline(r.buildManifestChunker())
	if err != nil {
		return errors.Wrap(err, "build manifest pipeline")
	}
	codePipe, err := r.buildPipeline(r.buildCodeChunker())
	if err != nil {
		return errors.Wrap(err, "build code pipeline")
	}
	commitsPipe, err := r.buildPipeline(r.buildCommitChunker())
	if err != nil {
		return errors.Wrap(err, "build commits pipeline")
	}

	anyErr := false

	for _, s := range sources {
		s, err = gitingest.Prepare(ctx, s, gitingest.SyncOptions{
			WorkDir: cfg.Git.WorkDir,
			Token:   cfg.Git.Token,
			Proxy:   cfg.Proxies.Git,
		})
		if err != nil {
			lg.Error("prepare git repo failed", zap.Error(err), zap.String("repo", s.Repo))
			anyErr = true
			continue
		}

		// Walk all enabled content types in one pass
		allDocs, err := gitingest.WalkAll(ctx, []gitingest.Source{s}, gitingest.WalkOptions{})
		if err != nil {
			lg.Error("git walk failed", zap.Error(err), zap.String("repo", s.Repo))
			anyErr = true
			continue
		}

		// Split walked documents by source prefix
		docs := filterDocsBySource(allDocs, index.SourceGitDocsPrefix)
		manifests := filterDocsBySource(allDocs, index.SourceGitManifestPrefix)
		codeDocs := filterDocsBySource(allDocs, index.SourceGitCodePrefix)

		// --- Markdown docs ---
		docsSrc := index.SourceGitDocs(s.Repo)
		if reset {
			if err := resetSource(ctx, db, vectors, docsSrc); err != nil {
				return err
			}
		}

		walkedIDs := make([]string, 0, len(docs))
		for _, d := range docs {
			walkedIDs = append(walkedIDs, d.SourceID)
		}

		batch := docs
		if limit > 0 && limit < len(batch) {
			batch = batch[:limit]
		}
		processed, errFound := indexBatch(ctx, lg, docsPipe, batch, dry, "git docs")
		if errFound {
			anyErr = true
		}

		if !dry && prune && limit <= 0 {
			if err := r.pruneOrphans(ctx, docsSrc, walkedIDs); err != nil {
				lg.Error("prune git docs orphans failed", zap.Error(err), zap.String("repo", s.Repo))
				anyErr = true
			}
		}

		status := "ok"
		if anyErr && !dry {
			status = "error"
		}
		if err := upsertSyncState(ctx, db, string(docsSrc), time.Now(), "", status, processed); err != nil {
			return errors.Wrap(err, "upsert syncstate")
		}

		// --- YAML manifests ---
		if s.Manifests {
			manifestSrc := index.SourceGitManifest(s.Repo)
			if reset {
				if err := resetSource(ctx, db, vectors, manifestSrc); err != nil {
					return err
				}
			}

			walkedManifestIDs := make([]string, 0, len(manifests))
			for _, d := range manifests {
				walkedManifestIDs = append(walkedManifestIDs, d.SourceID)
			}

			manifestBatch := manifests
			if limit > 0 && limit < len(manifestBatch) {
				manifestBatch = manifestBatch[:limit]
			}
			manifestProcessed, manifestErr := indexBatch(ctx, lg, manifestPipe, manifestBatch, dry, "git manifests")
			if manifestErr {
				anyErr = true
			}

			if !dry && prune && limit <= 0 {
				if err := r.pruneOrphans(ctx, manifestSrc, walkedManifestIDs); err != nil {
					lg.Error("prune git manifests orphans failed", zap.Error(err), zap.String("repo", s.Repo))
					anyErr = true
				}
			}

			mStatus := "ok"
			if anyErr && !dry {
				mStatus = "error"
			}
			if err := upsertSyncState(ctx, db, string(manifestSrc), time.Now(), "", mStatus, manifestProcessed); err != nil {
				return errors.Wrap(err, "upsert syncstate")
			}
		}

		// --- Source code ---
		if s.Code {
			codeSrc := index.SourceGitCode(s.Repo)
			if reset {
				if err := resetSource(ctx, db, vectors, codeSrc); err != nil {
					return err
				}
			}

			walkedCodeIDs := make([]string, 0, len(codeDocs))
			for _, d := range codeDocs {
				walkedCodeIDs = append(walkedCodeIDs, d.SourceID)
			}

			codeBatch := codeDocs
			if limit > 0 && limit < len(codeBatch) {
				codeBatch = codeBatch[:limit]
			}
			codeProcessed, codeErr := indexBatch(ctx, lg, codePipe, codeBatch, dry, "git code")
			if codeErr {
				anyErr = true
			}

			if !dry && prune && limit <= 0 {
				if err := r.pruneOrphans(ctx, codeSrc, walkedCodeIDs); err != nil {
					lg.Error("prune git code orphans failed", zap.Error(err), zap.String("repo", s.Repo))
					anyErr = true
				}
			}

			cStatus := "ok"
			if anyErr && !dry {
				cStatus = "error"
			}
			if err := upsertSyncState(ctx, db, string(codeSrc), time.Now(), "", cStatus, codeProcessed); err != nil {
				return errors.Wrap(err, "upsert syncstate")
			}
		}

		// --- Commits ---
		if s.Commits {
			commitsSrc := index.SourceGitCommit(s.Repo)
			if reset {
				if err := resetSource(ctx, db, vectors, commitsSrc); err != nil {
					return err
				}
			}

			cur, _ := loadGitCursor(ctx, db, string(commitsSrc))

			res, err := gitingest.WalkCommits(ctx, s, cur, limit)
			if err != nil {
				lg.Error("git walk commits failed", zap.Error(err), zap.String("repo", s.Repo))
				anyErr = true
				continue
			}

			commitBatch := res.Documents
			if limit > 0 && limit < len(commitBatch) {
				commitBatch = commitBatch[:limit]
			}
			processed, errFound := indexBatch(ctx, lg, commitsPipe, commitBatch, dry, "git commits")
			if errFound {
				anyErr = true
			}

			cursorJSON, _ := gitingest.MarshalCursor(res.NextCursor)
			status := "ok"
			if anyErr && !dry {
				status = "error"
			}
			if err := upsertSyncState(ctx, db, string(commitsSrc), time.Now(), string(cursorJSON), status, processed); err != nil {
				return errors.Wrap(err, "upsert syncstate")
			}
		}

		// --- Tags ---
		if s.Tags {
			tagsSrc := index.SourceGitTag(s.Repo)
			if reset {
				if err := resetSource(ctx, db, vectors, tagsSrc); err != nil {
					return err
				}
			}

			tags, err := gitingest.WalkTags(ctx, s)
			if err != nil {
				lg.Error("git walk tags failed", zap.Error(err), zap.String("repo", s.Repo))
				anyErr = true
				continue
			}

			walkedTagIDs := make([]string, 0, len(tags))
			for _, d := range tags {
				walkedTagIDs = append(walkedTagIDs, d.SourceID)
			}

			batch := tags
			if limit > 0 && limit < len(batch) {
				batch = batch[:limit]
			}
			processed, errFound := indexBatch(ctx, lg, commitsPipe, batch, dry, "git tags")
			if errFound {
				anyErr = true
			}

			if !dry && prune && limit <= 0 {
				if err := r.pruneOrphans(ctx, tagsSrc, walkedTagIDs); err != nil {
					lg.Error("prune git tags orphans failed", zap.Error(err), zap.String("repo", s.Repo))
					anyErr = true
				}
			}

			status := "ok"
			if anyErr && !dry {
				status = "error"
			}
			if err := upsertSyncState(ctx, db, string(tagsSrc), time.Now(), "", status, processed); err != nil {
				return errors.Wrap(err, "upsert syncstate")
			}
		}
	}

	if anyErr {
		return errors.New("one or more git documents failed to index")
	}
	return nil
}

// filterDocsBySource filters documents whose source has the given prefix.
func filterDocsBySource(docs []index.Document, prefix string) []index.Document {
	var out []index.Document
	for _, d := range docs {
		if strings.HasPrefix(string(d.Source), prefix) {
			out = append(out, d)
		}
	}
	return out
}

func (r *runner) runFiles(ctx context.Context, reset bool, limit int, dry bool) error {
	lg := zctx.From(ctx).Named("files")
	sources := fileSources(r.cfg.ContextFiles)
	if len(sources) == 0 {
		lg.Info("context files not configured, zero sources")
		return errNotConfigured
	}

	pipe, err := r.buildPipeline(r.buildDocChunker())
	if err != nil {
		return errors.Wrap(err, "build files pipeline")
	}

	anyErr := false
	for _, src := range sources {
		docs, err := filesingest.Walk(ctx, []filesingest.Source{src})
		if err != nil {
			lg.Error("walk context files failed", zap.Error(err), zap.String("source", src.Name))
			anyErr = true
			continue
		}

		indexSource := index.SourceContextFiles(src.Name)
		if reset {
			if err := resetSource(ctx, r.db, r.vectors, indexSource); err != nil {
				return err
			}
		}

		walkedIDs := make([]string, 0, len(docs))
		for _, d := range docs {
			walkedIDs = append(walkedIDs, d.SourceID)
		}

		batch := docs
		if limit > 0 && limit < len(batch) {
			batch = batch[:limit]
		}
		processed, errFound := indexBatch(ctx, lg, pipe, batch, dry, "context files")
		if errFound {
			anyErr = true
		}

		if !dry && limit <= 0 {
			if err := r.pruneOrphans(ctx, indexSource, walkedIDs); err != nil {
				lg.Error("prune context files orphans failed", zap.Error(err), zap.String("source", src.Name))
				anyErr = true
			}
		}

		status := "ok"
		if anyErr && !dry {
			status = "error"
		}
		if err := upsertSyncState(ctx, r.db, string(indexSource), time.Now(), "", status, processed); err != nil {
			return errors.Wrap(err, "upsert syncstate")
		}
	}

	if anyErr {
		return errors.New("one or more context files failed to index")
	}
	return nil
}

func (r *runner) buildDocChunker() index.Chunker {
	return chunkmd.New(chunkmd.ChunkerOptions{})
}

func (r *runner) buildManifestChunker() index.Chunker {
	return chunkyaml.New(chunkyaml.ChunkerOptions{})
}

func (r *runner) buildCodeChunker() index.Chunker {
	return chunkcode.New(chunkcode.ChunkerOptions{})
}

func (r *runner) buildCommitChunker() index.Chunker {
	return chunkgit.New()
}

func (r *runner) buildPipeline(ch index.Chunker) (*pipeline.Pipeline, error) {
	return pipeline.New(r.db, ch, r.embedder, r.vectors, pipeline.PipelineOptions{
		TracerProvider: r.tp,
		MeterProvider:  r.mp,
	})
}

func (r *runner) pruneOrphans(ctx context.Context, src index.Source, walkedSourceIDs []string) error {
	lg := zctx.From(ctx).Named("prune")
	db := r.db

	orphanChunkIDs, err := db.Chunk.Query().
		Where(chunk.HasDocumentWith(
			document.Source(string(src)),
			document.SourceIDNotIn(walkedSourceIDs...),
		)).
		IDs(ctx)
	if err != nil {
		return errors.Wrap(err, "query orphan chunks")
	}

	if len(orphanChunkIDs) == 0 {
		return nil
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return errors.Wrap(err, "tx begin for prune")
	}
	if _, err := tx.Chunk.Delete().
		Where(chunk.HasDocumentWith(
			document.Source(string(src)),
			document.SourceIDNotIn(walkedSourceIDs...),
		)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "delete orphan chunks")
	}
	n, err := tx.Document.Delete().
		Where(
			document.Source(string(src)),
			document.SourceIDNotIn(walkedSourceIDs...),
		).
		Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "delete orphan documents")
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit prune")
	}

	lg.Info("pruned orphan documents",
		zap.String("source", string(src)),
		zap.Int("documents", n),
		zap.Int("chunks", len(orphanChunkIDs)))

	if r.vectors != nil {
		const batch = 1000
		for i := 0; i < len(orphanChunkIDs); i += batch {
			j := min(i+batch, len(orphanChunkIDs))
			if derr := r.vectors.Delete(ctx, orphanChunkIDs[i:j]); derr != nil {
				lg.Warn("qdrant delete for orphan chunks (non-fatal)",
					zap.Error(derr),
					zap.Int("count", j-i),
					zap.String("source", string(src)))
			}
		}
	}
	return nil
}

func (r *runner) runGitLabAPI(ctx context.Context, p *pipeline.Pipeline, since time.Time, reset bool, limit int, dry bool) error {
	lg := zctx.From(ctx).Named("gitlab")
	cfg := r.cfg
	db := r.db
	vectors := r.vectors

	projects := gitLabProjectRefs(cfg.GitLab.Projects)
	if cfg.GitLab.BaseURL == "" || cfg.GitLab.Token == "" || len(projects) == 0 {
		lg.Info("gitlab not configured")
		return errNotConfigured
	}

	cache, err := authenticatedHTTPCache("gitlab", cfg.GitLab.BaseURL, cfg.GitLab.Token)
	if err != nil {
		return errors.Wrap(err, "gitlab http cache")
	}

	httpClient, err := netclient.HTTPClient(ctx, "gitlab", cfg.Proxies.GitLab, netclient.HTTPClientOptions{
		TracerProvider: r.tp,
		MeterProvider:  r.mp,
		Cache:          cache,
	})
	if err != nil {
		return errors.Wrap(err, "gitlab http client")
	}

	fetcher, err := gitlabingest.New(gitlabingest.Options{
		BaseURL:    cfg.GitLab.BaseURL,
		Token:      cfg.GitLab.Token,
		Projects:   projects,
		HTTPClient: httpClient,
	})
	if err != nil {
		return errors.Wrap(err, "gitlab new fetcher")
	}

	if err := fetcher.CheckAuth(ctx); err != nil {
		return errors.Wrap(err, "gitlab auth check")
	}

	// Helper to ingest a resource type
	ingestResource := func(resourceName string, enabled bool, srcGenerator func() index.Source, fetch func(context.Context, int, gitlabingest.Cursor) (gitlabingest.FetchResult, error)) error {
		if !enabled {
			return nil
		}

		src := srcGenerator()
		if reset {
			if err := resetSource(ctx, db, vectors, src); err != nil {
				return err
			}
		}
		lg := lg.WithLazy(
			zap.String("src", string(src)),
			zap.String("resource", resourceName),
		)

		startCur, _ := loadGitLabCursor(ctx, db, string(src))
		if !since.IsZero() {
			startCur.UpdatedAfter = since.Format(time.RFC3339)
		}

		processed := 0
		anyErr := false
		page := 1
		limReached := false
		// Keep the request cursor (updated_after) FIXED while paginating by page;
		// track the max updated_at observed across pages and persist that as the
		// cursor for the next run. Advancing updated_after mid-pagination would
		// skip records.
		maxObserved := startCur.UpdatedAfter

		for {
			res, err := fetch(ctx, page, startCur)
			if err != nil {
				lg.Error("gitlab fetch failed", zap.Error(err))
				anyErr = true
				break
			}

			pageBatch := res.Documents
			if limit > 0 {
				remaining := limit - processed
				if remaining <= 0 {
					limReached = true
					pageBatch = nil
				} else if remaining < len(pageBatch) {
					pageBatch = pageBatch[:remaining]
					limReached = true
				}
			}
			n, errFound := indexBatch(ctx, lg, p, pageBatch, dry, "gitlab "+resourceName)
			processed += n
			if errFound {
				anyErr = true
			}

			if res.NextCursor.UpdatedAfter > maxObserved {
				maxObserved = res.NextCursor.UpdatedAfter
			}
			if limReached || !res.HasMore {
				break
			}
			page++
		}

		curStr, _ := json.Marshal(gitlabingest.Cursor{UpdatedAfter: maxObserved})
		status := "ok"
		if anyErr && !dry {
			status = "error"
		}
		if err := upsertSyncState(ctx, db, string(src), time.Now(), string(curStr), status, processed); err != nil {
			return errors.Wrap(err, "upsert syncstate")
		}

		if anyErr {
			return errors.New("one or more " + resourceName + " documents failed to index")
		}
		return nil
	}

	// Process issues
	if err := ingestResource("issues", cfg.GitLab.Issues,
		func() index.Source { return index.SourceGitLabIssue },
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchIssues(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, errNotConfigured) {
		return err
	}

	// Process merge requests
	if err := ingestResource("merge_requests", cfg.GitLab.MergeRequests,
		func() index.Source { return index.SourceGitLabMR },
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchMergeRequests(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, errNotConfigured) {
		return err
	}

	// Process releases
	if err := ingestResource("releases", cfg.GitLab.Releases,
		func() index.Source { return index.SourceGitLabRelease },
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchReleases(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, errNotConfigured) {
		return err
	}

	return nil
}

func (r *runner) runJira(ctx context.Context, p *pipeline.Pipeline, since time.Time, reset bool, limit int, dry bool) error {
	lg := zctx.From(ctx).Named("jira")
	cfg := r.cfg
	db := r.db
	vectors := r.vectors

	jc := cfg.Jira
	if jc.BaseURL == "" || (jc.PAT == "" && (jc.Username == "" || jc.Password == "") && (jc.Email == "" || jc.APIToken == "")) {
		lg.Info("jira not configured")
		return errNotConfigured
	}

	cache, err := authenticatedHTTPCache("jira", jc.BaseURL, jc.Email, jc.Username, jc.APIToken, jc.Password, jc.PAT)
	if err != nil {
		return errors.Wrap(err, "jira http cache")
	}

	src := index.SourceJira
	httpClient, err := netclient.HTTPClient(ctx, "jira", cfg.Proxies.Jira, netclient.HTTPClientOptions{
		TracerProvider: r.tp,
		MeterProvider:  r.mp,
		Cache:          cache,
	})
	if err != nil {
		return errors.Wrap(err, "jira http client")
	}
	fetcher, err := jiraingest.New(jiraingest.Options{
		BaseURL:    jc.BaseURL,
		Email:      jc.Email,
		Username:   jc.Username,
		APIToken:   jc.APIToken,
		Password:   jc.Password,
		PAT:        jc.PAT,
		HTTPClient: httpClient,
	})
	if err != nil {
		return errors.Wrap(err, "jira new fetcher")
	}
	projects := jiraProjectKeys(jc.Projects)
	authStatus, err := fetcher.CheckAuth(ctx, projects)
	if err != nil {
		return errors.Wrap(err, "jira preflight")
	}
	lg.Info("jira auth ok",
		zap.String("account_id", authStatus.AccountID),
		zap.String("name", authStatus.Name),
		zap.String("display_name", authStatus.DisplayName),
	)

	if reset {
		if err := resetSource(ctx, db, vectors, src); err != nil {
			return err
		}
	}

	cur, _ := loadJiraCursor(ctx, db, string(src))
	if !since.IsZero() {
		cur.LastUpdated = since.Format(time.RFC3339)
		cur.StartAt = 0
	}

	processed := 0
	anyErr := false
	finalCur := cur

	_, fetchErr := fetcher.FetchAll(ctx, jiraingest.FetchOptions{
		Projects: projects,
		PageSize: 100,
	}, cur, func(ctx context.Context, docs []index.Document, nextCur jiraingest.Cursor) error {
		if limit > 0 && processed >= limit {
			return errLimitReached
		}
		batch := docs
		if limit > 0 {
			if remaining := limit - processed; remaining < len(batch) {
				batch = batch[:remaining]
			}
		}
		n, errFound := indexBatch(ctx, lg, p, batch, dry, "jira")
		processed += n
		if errFound {
			anyErr = true
		}
		curStr, _ := json.Marshal(nextCur)
		now := time.Now()
		st := "ok"
		if anyErr && !dry {
			st = "error"
		}
		_ = upsertSyncState(ctx, db, string(src), now, string(curStr), st, processed)
		finalCur = nextCur
		return nil
	})

	if errors.Is(fetchErr, errLimitReached) {
		fetchErr = nil
	}
	if fetchErr != nil {
		anyErr = true
	}

	curStr, _ := json.Marshal(finalCur)
	st := "ok"
	if anyErr && !dry {
		st = "error"
	}
	_ = upsertSyncState(ctx, db, string(src), time.Now(), string(curStr), st, processed)

	if fetchErr != nil {
		return errors.Wrap(fetchErr, "jira fetchall")
	}
	return nil
}

func (r *runner) runTelegram(ctx context.Context, p *pipeline.Pipeline, since time.Time, reset bool, limit int, dry bool) error {
	tc := r.cfg.Telegram
	lg := zctx.From(ctx).Named("telegram")
	db := r.db
	vectors := r.vectors

	if tc.AppID == 0 || tc.AppHash == "" || tc.IngestSession == "" {
		lg.Info("telegram not configured")
		return errNotConfigured
	}
	if _, err := os.Stat(tc.IngestSession); err != nil {
		return errors.Wrap(err, "telegram ingest session file not found")
	}

	src := index.SourceTelegram
	if reset {
		if err := resetSource(ctx, db, vectors, src); err != nil {
			return err
		}
	}

	tgClient := gotdtelegram.NewClient(tc.AppID, tc.AppHash, gotdtelegram.Options{
		Logger:         logzap.New(lg.Named("td").Named("ingest")),
		SessionStorage: &gotdtelegram.FileSessionStorage{Path: tc.IngestSession},
		TracerProvider: r.tp,
		Middlewares:    []gotdtelegram.Middleware{telemetry.TDMiddleware(r.tp, r.mp)},
	})

	var result telegramingest.BackfillResult
	var backfillErr error

	runErr := tgClient.Run(ctx, func(ctx context.Context) error {
		bf, err := telegramingest.NewBackfiller(db, telegramingest.BackfillOptions{
			Session: tgClient,
		})
		if err != nil {
			return errors.Wrap(err, "new backfiller")
		}

		chats := telegramChats(tc.MonitorChats)
		if len(chats) == 0 {
			lg.Info("telegram: no monitor chats; nothing to do")
			return nil
		}

		cur, _ := loadTelegramCursor(ctx, db, string(src))
		if !since.IsZero() {
			lg.Info("since ignored for telegram")
			cur = telegramingest.Cursor{}
		}

		req := telegramingest.BackfillRequest{
			Chats:  chats,
			Cursor: cur,
			Limit:  limit,
		}
		result, backfillErr = bf.Backfill(ctx, req)
		if backfillErr != nil {
			return errors.Wrap(backfillErr, "backfill")
		}
		return nil
	})
	if runErr != nil {
		return errors.Wrap(runErr, "telegram client run")
	}

	processed, anyErr := indexBatch(ctx, lg, p, result.Documents, dry, "telegram")

	nextCurStr := ""
	if result.NextCursor.PerChat != nil {
		if s, err := result.NextCursor.Encode(); err == nil {
			nextCurStr = s
		}
	}
	now := time.Now()
	st := "ok"
	if anyErr && !dry {
		st = "error"
	}
	count := len(result.Documents)
	if count == 0 {
		count = processed
	}
	_ = upsertSyncState(ctx, db, string(src), now, nextCurStr, st, count)

	return nil
}

func resetSource(ctx context.Context, db *ent.Client, vectors pipeline.VectorStore, src index.Source) error {
	lg := zctx.From(ctx)
	chunkIDs, err := db.Chunk.Query().
		Where(chunk.HasDocumentWith(document.Source(string(src)))).
		IDs(ctx)
	if err != nil {
		return errors.Wrap(err, "query chunks for reset")
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return errors.Wrap(err, "tx begin for reset")
	}
	if _, err := tx.Chunk.Delete().
		Where(chunk.HasDocumentWith(document.Source(string(src)))).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "delete chunks")
	}
	if _, err := tx.Document.Delete().
		Where(document.Source(string(src))).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "delete documents")
	}
	if _, err := tx.SyncState.Delete().
		Where(syncstate.Source(string(src))).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "delete syncstate")
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit reset")
	}

	if vectors != nil && len(chunkIDs) > 0 {
		const batch = 1000
		for i := 0; i < len(chunkIDs); i += batch {
			j := min(i+batch, len(chunkIDs))
			b := chunkIDs[i:j]
			if derr := vectors.Delete(ctx, b); derr != nil {
				lg.Warn("qdrant delete during reset (non-fatal)",
					zap.Error(derr),
					zap.Int("count", len(b)),
					zap.String("source", string(src)))
			}
		}
	} else if vectors == nil && len(chunkIDs) > 0 {
		lg.Warn("Qdrant unavailable; ent data cleared but vector points remain — they won't match any deleted doc",
			zap.String("source", string(src)))
	}

	lg.Info("reset done", zap.String("source", string(src)), zap.Int("chunks", len(chunkIDs)))
	return nil
}

func upsertSyncState(ctx context.Context, db *ent.Client, src string, lastSynced time.Time, lastCursor, status string, docCount int) error {
	return db.SyncState.Create().
		SetSource(src).
		SetLastSyncedAt(lastSynced).
		SetLastCursor(lastCursor).
		SetStatus(status).
		SetDocumentCount(docCount).
		OnConflictColumns("source").
		UpdateNewValues().
		Exec(ctx)
}

func gitSources(sources []config.GitSource) []gitingest.Source {
	out := make([]gitingest.Source, 0, len(sources))
	for _, src := range sources {
		out = append(out, gitingest.Source{
			Root:            src.Root,
			URL:             src.URL,
			Repo:            src.Repo,
			Branch:          src.Branch,
			BaseURL:         src.BaseURL,
			Include:         src.Include,
			Exclude:         src.Exclude,
			Commits:         src.Commits,
			Tags:            src.Tags,
			Manifests:       src.Manifests,
			Code:            src.Code,
			ManifestExclude: src.ManifestExclude,
			CodeInclude:     src.CodeInclude,
			CodeExclude:     src.CodeExclude,
		})
	}
	return out
}

func fileSources(sources []config.ContextFileSource) []filesingest.Source {
	out := make([]filesingest.Source, 0, len(sources))
	for _, src := range sources {
		out = append(out, filesingest.Source{
			Name:      src.Name,
			Root:      src.Root,
			BaseURL:   src.BaseURL,
			Include:   src.Include,
			Exclude:   src.Exclude,
			Authority: src.Authority,
		})
	}
	return out
}

func loadGitCursor(ctx context.Context, db *ent.Client, src string) (gitingest.CommitCursor, error) {
	ss, err := db.SyncState.Query().Where(syncstate.Source(src)).Only(ctx)
	if ent.IsNotFound(err) {
		return gitingest.CommitCursor{}, nil
	}
	if err != nil {
		return gitingest.CommitCursor{}, errors.Wrap(err, "query syncstate")
	}
	var c gitingest.CommitCursor
	if ss.LastCursor != "" {
		if uerr := json.Unmarshal([]byte(ss.LastCursor), &c); uerr != nil {
			return gitingest.CommitCursor{}, nil
		}
	}
	return c, nil
}

func loadGitLabCursor(ctx context.Context, db *ent.Client, src string) (gitlabingest.Cursor, error) {
	ss, err := db.SyncState.Query().Where(syncstate.Source(src)).Only(ctx)
	if ent.IsNotFound(err) {
		return gitlabingest.Cursor{}, nil
	}
	if err != nil {
		return gitlabingest.Cursor{}, errors.Wrap(err, "query syncstate")
	}
	var c gitlabingest.Cursor
	if ss.LastCursor != "" {
		if uerr := json.Unmarshal([]byte(ss.LastCursor), &c); uerr != nil {
			return gitlabingest.Cursor{}, nil
		}
	}
	return c, nil
}

func loadJiraCursor(ctx context.Context, db *ent.Client, src string) (jiraingest.Cursor, error) {
	ss, err := db.SyncState.Query().Where(syncstate.Source(src)).Only(ctx)
	if ent.IsNotFound(err) {
		return jiraingest.Cursor{}, nil
	}
	if err != nil {
		return jiraingest.Cursor{}, errors.Wrap(err, "query syncstate")
	}
	var c jiraingest.Cursor
	if ss.LastCursor != "" {
		if uerr := json.Unmarshal([]byte(ss.LastCursor), &c); uerr != nil {
			return jiraingest.Cursor{}, nil
		}
	}
	return c, nil
}

func loadTelegramCursor(ctx context.Context, db *ent.Client, src string) (telegramingest.Cursor, error) {
	ss, err := db.SyncState.Query().Where(syncstate.Source(src)).Only(ctx)
	if ent.IsNotFound(err) {
		return telegramingest.Cursor{}, nil
	}
	if err != nil {
		return telegramingest.Cursor{}, errors.Wrap(err, "query syncstate")
	}
	if ss.LastCursor == "" {
		return telegramingest.Cursor{}, nil
	}
	return telegramingest.DecodeCursor(ss.LastCursor)
}

func gitLabProjectRefs(projects []config.GitLabProject) []string {
	out := make([]string, 0, len(projects))
	for _, project := range projects {
		if project.Ref != "" {
			out = append(out, project.Ref)
		}
	}
	return out
}

func jiraProjectKeys(projects []config.JiraProject) []string {
	out := make([]string, 0, len(projects))
	for _, project := range projects {
		if project.Key != "" {
			out = append(out, project.Key)
		}
	}
	return out
}

func telegramChats(chats []config.TelegramChat) []telegramingest.ChatSpec {
	out := make([]telegramingest.ChatSpec, 0, len(chats))
	for _, chat := range chats {
		if chat.ID == 0 {
			continue
		}
		out = append(out, telegramingest.ChatSpec{
			ID:       chat.ID,
			Username: chat.Username,
			Limit:    chat.Limit,
		})
	}
	return out
}
