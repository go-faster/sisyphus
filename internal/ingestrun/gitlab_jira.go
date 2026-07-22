// Package ingestrun contains shared ingestion runners used by ssingest and
// webhook-triggered ssapi refreshes.
package ingestrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/gregjones/httpcache/diskcache"
	"github.com/peterbourgon/diskv"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/ent/syncstate"
	"github.com/go-faster/sisyphus/internal/index"
	gitlabingest "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/netclient"
	"github.com/go-faster/sisyphus/internal/pipeline"
)

var (
	// ErrNotConfigured reports that a source lacks required configuration.
	ErrNotConfigured = errors.New("source not configured")
	errLimitReached  = errors.New("limit reached")
)

// indexConcurrency bounds how many documents pipeline.Index runs at once.
const indexConcurrency = 8

// Runner runs ingestion sources against the shared pipeline infrastructure.
type Runner struct {
	DB        *ent.Client
	Vectors   pipeline.VectorStore
	Embedder  index.Embedder
	Config    config.Config
	TP        trace.TracerProvider
	MP        metric.MeterProvider
	UserAgent string
}

// Pipeline builds an indexing pipeline for a source chunker.
func (r Runner) Pipeline(ch index.Chunker) (*pipeline.Pipeline, error) {
	return pipeline.New(r.DB, ch, r.Embedder, r.Vectors, pipeline.PipelineOptions{
		TracerProvider: r.TP,
		MeterProvider:  r.MP,
	})
}

// GitLabOptions controls a GitLab ingestion run.
type GitLabOptions struct {
	Pipeline *pipeline.Pipeline
	Since    time.Time
	Reset    bool
	Limit    int
	DryRun   bool
}

// JiraOptions controls a Jira ingestion run.
type JiraOptions struct {
	Pipeline *pipeline.Pipeline
	Since    time.Time
	Reset    bool
	Limit    int
	DryRun   bool
}

// RunGitLab runs incremental GitLab ingestion for all enabled GitLab resources.
func (r Runner) RunGitLab(ctx context.Context, opts GitLabOptions) error {
	lg := zctx.From(ctx).Named("gitlab")
	cfg := r.Config

	projects := GitLabProjectRefs(cfg.GitLab.Projects)
	if cfg.GitLab.BaseURL == "" || cfg.GitLab.Token == "" || len(projects) == 0 {
		lg.Info("gitlab not configured")
		return ErrNotConfigured
	}

	cache, err := AuthenticatedHTTPCache("gitlab", cfg.GitLab.BaseURL, cfg.GitLab.Token)
	if err != nil {
		return errors.Wrap(err, "gitlab http cache")
	}

	httpClient, err := netclient.HTTPClient(ctx, "gitlab", cfg.Proxies.GitLab, netclient.HTTPClientOptions{
		TracerProvider: r.TP,
		MeterProvider:  r.MP,
		Cache:          cache,
		UserAgent:      r.UserAgent,
	})
	if err != nil {
		return errors.Wrap(err, "gitlab http client")
	}

	fetcher, err := gitlabingest.New(gitlabingest.Options{
		BaseURL:    cfg.GitLab.BaseURL,
		Token:      cfg.GitLab.Token,
		Projects:   projects,
		HTTPClient: httpClient,
		UserAgent:  r.UserAgent,
	})
	if err != nil {
		return errors.Wrap(err, "gitlab new fetcher")
	}

	if err := fetcher.CheckAuth(ctx); err != nil {
		return errors.Wrap(err, "gitlab auth check")
	}

	pipe := opts.Pipeline
	if pipe == nil {
		pipe, err = r.Pipeline(chunkgitlab.New())
		if err != nil {
			return errors.Wrap(err, "build gitlab pipeline")
		}
	}

	ingestResource := func(resourceName string, enabled bool, src index.Source, fetch func(context.Context, int, gitlabingest.Cursor) (gitlabingest.FetchResult, error)) error {
		if !enabled {
			return nil
		}
		if opts.Reset {
			if err := ResetSource(ctx, r.DB, r.Vectors, src); err != nil {
				return err
			}
		}

		lg := lg.WithLazy(
			zap.String("src", string(src)),
			zap.String("resource", resourceName),
		)
		startCur, _ := LoadGitLabCursor(ctx, r.DB, string(src))
		if !opts.Since.IsZero() {
			startCur.UpdatedAfter = opts.Since.Format(time.RFC3339)
		}

		processed := 0
		anyErr := false
		page := 1
		limReached := false
		maxObserved := startCur.UpdatedAfter

		for {
			res, err := fetch(ctx, page, startCur)
			if err != nil {
				lg.Error("gitlab fetch failed", zap.Error(err))
				anyErr = true
				break
			}

			pageBatch := res.Documents
			if opts.Limit > 0 {
				remaining := opts.Limit - processed
				if remaining <= 0 {
					limReached = true
					pageBatch = nil
				} else if remaining < len(pageBatch) {
					pageBatch = pageBatch[:remaining]
					limReached = true
				}
			}
			n, errFound := IndexBatch(ctx, lg, pipe, pageBatch, opts.DryRun, "gitlab "+resourceName)
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
		if anyErr && !opts.DryRun {
			status = "error"
		}
		if err := UpsertSyncState(ctx, r.DB, string(src), time.Now(), string(curStr), status, processed); err != nil {
			return errors.Wrap(err, "upsert syncstate")
		}

		if anyErr {
			return errors.New("one or more " + resourceName + " documents failed to index")
		}
		return nil
	}

	if err := ingestResource("issues", cfg.GitLab.Issues, index.SourceGitLabIssue,
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchIssues(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, ErrNotConfigured) {
		return err
	}
	if err := ingestResource("merge_requests", cfg.GitLab.MergeRequests, index.SourceGitLabMR,
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchMergeRequests(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, ErrNotConfigured) {
		return err
	}
	if err := ingestResource("releases", cfg.GitLab.Releases, index.SourceGitLabRelease,
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchReleases(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, ErrNotConfigured) {
		return err
	}

	return nil
}

// RunJira runs incremental Jira ingestion.
func (r Runner) RunJira(ctx context.Context, opts JiraOptions) error {
	lg := zctx.From(ctx).Named("jira")
	cfg := r.Config
	jc := cfg.Jira
	if jc.BaseURL == "" || (jc.PAT == "" && (jc.Username == "" || jc.Password == "") && (jc.Email == "" || jc.APIToken == "")) {
		lg.Info("jira not configured")
		return ErrNotConfigured
	}

	cache, err := AuthenticatedHTTPCache("jira", jc.BaseURL, jc.Email, jc.Username, jc.APIToken, jc.Password, jc.PAT)
	if err != nil {
		return errors.Wrap(err, "jira http cache")
	}

	src := index.SourceJira
	httpClient, err := netclient.HTTPClient(ctx, "jira", cfg.Proxies.Jira, netclient.HTTPClientOptions{
		TracerProvider: r.TP,
		MeterProvider:  r.MP,
		Cache:          cache,
		UserAgent:      r.UserAgent,
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
		UserAgent:  r.UserAgent,
	})
	if err != nil {
		return errors.Wrap(err, "jira new fetcher")
	}
	projects := JiraProjectKeys(jc.Projects)
	authStatus, err := fetcher.CheckAuth(ctx, projects)
	if err != nil {
		return errors.Wrap(err, "jira preflight")
	}
	lg.Info("jira auth ok",
		zap.String("account_id", authStatus.AccountID),
		zap.String("name", authStatus.Name),
		zap.String("display_name", authStatus.DisplayName),
	)

	if opts.Reset {
		if err := ResetSource(ctx, r.DB, r.Vectors, src); err != nil {
			return err
		}
	}

	pipe := opts.Pipeline
	if pipe == nil {
		pipe, err = r.Pipeline(chunkjira.New())
		if err != nil {
			return errors.Wrap(err, "build jira pipeline")
		}
	}

	cur, _ := LoadJiraCursor(ctx, r.DB, string(src))
	if !opts.Since.IsZero() {
		cur.LastUpdated = opts.Since.Format(time.RFC3339)
		cur.StartAt = 0
	}

	processed := 0
	anyErr := false
	finalCur := cur

	_, fetchErr := fetcher.FetchAll(ctx, jiraingest.FetchOptions{
		Projects: projects,
		PageSize: 100,
	}, cur, func(ctx context.Context, docs []index.Document, nextCur jiraingest.Cursor) error {
		if opts.Limit > 0 && processed >= opts.Limit {
			return errLimitReached
		}
		batch := docs
		if opts.Limit > 0 {
			if remaining := opts.Limit - processed; remaining < len(batch) {
				batch = batch[:remaining]
			}
		}
		n, errFound := IndexBatch(ctx, lg, pipe, batch, opts.DryRun, "jira")
		processed += n
		if errFound {
			anyErr = true
		}
		curStr, _ := json.Marshal(nextCur)
		st := "ok"
		if anyErr && !opts.DryRun {
			st = "error"
		}
		_ = UpsertSyncState(ctx, r.DB, string(src), time.Now(), string(curStr), st, processed)
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
	if anyErr && !opts.DryRun {
		st = "error"
	}
	_ = UpsertSyncState(ctx, r.DB, string(src), time.Now(), string(curStr), st, processed)

	if fetchErr != nil {
		return errors.Wrap(fetchErr, "jira fetchall")
	}
	return nil
}

// IndexBatch runs p.Index over docs with bounded concurrency.
func IndexBatch(ctx context.Context, lg *zap.Logger, p *pipeline.Pipeline, docs []index.Document, dry bool, label string) (processed int, anyErr bool) {
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

// ResetSource deletes a source's documents, chunks, sync state, and vector points.
func ResetSource(ctx context.Context, db *ent.Client, vectors pipeline.VectorStore, src index.Source) error {
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
		lg.Warn("qdrant unavailable; ent data cleared but vector points remain; they won't match any deleted doc",
			zap.String("source", string(src)))
	}

	lg.Info("reset done", zap.String("source", string(src)), zap.Int("chunks", len(chunkIDs)))
	return nil
}

// UpsertSyncState records source ingestion cursor state.
func UpsertSyncState(ctx context.Context, db *ent.Client, src string, lastSynced time.Time, lastCursor, status string, docCount int) error {
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

// LoadGitLabCursor loads a GitLab cursor from SyncState.
func LoadGitLabCursor(ctx context.Context, db *ent.Client, src string) (gitlabingest.Cursor, error) {
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

// LoadJiraCursor loads a Jira cursor from SyncState.
func LoadJiraCursor(ctx context.Context, db *ent.Client, src string) (jiraingest.Cursor, error) {
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

// LoadRawCursor loads a SyncState row's opaque last_cursor string, for
// callers (the notify collectors) whose cursor format isn't one of
// gitlabingest.Cursor/jiraingest.Cursor and so can't use
// LoadGitLabCursor/LoadJiraCursor.
func LoadRawCursor(ctx context.Context, db *ent.Client, src string) (string, error) {
	ss, err := db.SyncState.Query().Where(syncstate.Source(src)).Only(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", errors.Wrap(err, "query syncstate")
	}
	return ss.LastCursor, nil
}

// GitLabProjectRefs extracts non-empty GitLab project refs.
func GitLabProjectRefs(projects []config.GitLabProject) []string {
	out := make([]string, 0, len(projects))
	for _, project := range projects {
		if project.Ref != "" {
			out = append(out, project.Ref)
		}
	}
	return out
}

// JiraProjectKeys extracts non-empty Jira project keys.
func JiraProjectKeys(projects []config.JiraProject) []string {
	out := make([]string, 0, len(projects))
	for _, project := range projects {
		if project.Key != "" {
			out = append(out, project.Key)
		}
	}
	return out
}

// AuthenticatedHTTPCache returns a per-service per-auth cache.
func AuthenticatedHTTPCache(service string, authParts ...string) (*diskcache.Cache, error) {
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

var _ = sql.OrderDesc // keep entsql import available for future filters
