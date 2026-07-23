package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/chunk"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/ent/syncstate"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/indexjob"
	filesingest "github.com/go-faster/sisyphus/internal/ingest/files"
	gitingest "github.com/go-faster/sisyphus/internal/ingest/git"
	telegramingest "github.com/go-faster/sisyphus/internal/ingest/telegram"
	"github.com/go-faster/sisyphus/internal/ingestrun"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/telemetry"

	"github.com/gotd/log/logzap"
	gotdtelegram "github.com/gotd/td/telegram"
)

var errNotConfigured = ingestrun.ErrNotConfigured

// indexBatch runs p.Index over docs with bounded concurrency. A single
// document's failure is logged and does not stop the others, matching the
// sequential loops it replaces; anyErr reports whether at least one failed.
func indexBatch(ctx context.Context, lg *zap.Logger, p pipeline.Indexer, docs []index.Document, dry bool, label string) (processed int, anyErr bool) {
	return ingestrun.IndexBatch(ctx, lg, p, docs, dry, label)
}

type runner struct {
	db      *ent.Client
	vectors pipeline.VectorStore
	// sqlDB is the pooled handle behind db, used for the per-source advisory
	// lock: a session-scoped lock needs a connection of its own, which the ent
	// client does not hand out.
	sqlDB     *sql.DB
	cfg       config.Config
	tp        trace.TracerProvider
	mp        metric.MeterProvider
	embedder  index.Embedder
	userAgent string
	// newIndexer decides where indexing happens — in this process, or on a
	// worker via the queue. The runs below do not know which.
	newIndexer indexerFactory
}

// locked serializes a run against any other process running the same source.
//
// Only the fetch-and-advance half needs it: indexing is idempotent on
// (source, source_id), but a cursor and an orphan prune are both
// read-modify-write over shared rows, and two runs interleaving there either
// rewind the cursor or prune documents the other just wrote.
func (r *runner) locked(ctx context.Context, key string, fn func(context.Context) error) error {
	return ingestrun.WithSourceLock(ctx, r.sqlDB, key, fn)
}

func (r *runner) runGit(ctx context.Context, reset bool, limit int, dry, prune bool) error {
	return r.locked(ctx, "git", func(ctx context.Context) error {
		return r.runGitLocked(ctx, reset, limit, dry, prune)
	})
}

func (r *runner) runGitLocked(ctx context.Context, reset bool, limit int, dry, prune bool) error {
	lg := zctx.From(ctx).Named("git")
	cfg := r.cfg
	db := r.db
	vectors := r.vectors

	sources := gitSources(cfg.Git.Repos)
	if len(sources) == 0 {
		lg.Info("git not configured, zero sources")
		return errNotConfigured
	}

	// One indexer per content type: the walk splits into Markdown docs, YAML
	// manifests, source code and commits, each with its own chunker.
	docsPipe, err := r.newIndexer(indexjob.KindMarkdown)
	if err != nil {
		return err
	}
	manifestPipe, err := r.newIndexer(indexjob.KindYAML)
	if err != nil {
		return err
	}
	codePipe, err := r.newIndexer(indexjob.KindCode)
	if err != nil {
		return err
	}
	commitsPipe, err := r.newIndexer(indexjob.KindGit)
	if err != nil {
		return err
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
		allDocs, err := gitingest.WalkAll(ctx, []gitingest.Source{s}, gitingest.WalkOptions{MeterProvider: r.mp})
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
	return r.locked(ctx, "files", func(ctx context.Context) error {
		return r.runFilesLocked(ctx, reset, limit, dry)
	})
}

func (r *runner) runFilesLocked(ctx context.Context, reset bool, limit int, dry bool) error {
	lg := zctx.From(ctx).Named("files")
	sources := fileSources(r.cfg.ContextFiles)
	if len(sources) == 0 {
		lg.Info("context files not configured, zero sources")
		return errNotConfigured
	}

	pipe, err := r.newIndexer(indexjob.KindMarkdown)
	if err != nil {
		return err
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

func (r *runner) runGitLabAPI(ctx context.Context, since time.Time, reset bool, limit int, dry bool) error {
	idx, err := r.newIndexer(indexjob.KindGitLab)
	if err != nil {
		return err
	}
	return r.locked(ctx, string(index.SourceGitLabIssue), func(ctx context.Context) error {
		return r.sharedRunner().RunGitLab(ctx, ingestrun.GitLabOptions{
			Indexer: idx,
			Since:   since,
			Reset:   reset,
			Limit:   limit,
			DryRun:  dry,
		})
	})
}

func (r *runner) runJira(ctx context.Context, since time.Time, reset bool, limit int, dry bool) error {
	idx, err := r.newIndexer(indexjob.KindJira)
	if err != nil {
		return err
	}
	return r.locked(ctx, string(index.SourceJira), func(ctx context.Context) error {
		return r.sharedRunner().RunJira(ctx, ingestrun.JiraOptions{
			Indexer: idx,
			Since:   since,
			Reset:   reset,
			Limit:   limit,
			DryRun:  dry,
		})
	})
}

func (r *runner) sharedRunner() ingestrun.Runner {
	return ingestrun.Runner{
		DB:        r.db,
		Vectors:   r.vectors,
		Embedder:  r.embedder,
		Config:    r.cfg,
		TP:        r.tp,
		MP:        r.mp,
		UserAgent: r.userAgent,
	}
}

func (r *runner) runTelegram(ctx context.Context, since time.Time, reset bool, limit int, dry bool, dumpPaths []string) error {
	return r.locked(ctx, string(index.SourceTelegram), func(ctx context.Context) error {
		return r.runTelegramLocked(ctx, since, reset, limit, dry, dumpPaths)
	})
}

func (r *runner) runTelegramLocked(ctx context.Context, since time.Time, reset bool, limit int, dry bool, dumpPaths []string) error {
	tc := r.cfg.Telegram
	lg := zctx.From(ctx).Named("telegram")
	db := r.db
	vectors := r.vectors

	liveConfigured := tc.AppID != 0 && tc.AppHash != "" && tc.IngestSession != ""
	dumpConfigured := len(dumpPaths) > 0
	if !liveConfigured && !dumpConfigured {
		lg.Info("telegram not configured")
		return errNotConfigured
	}

	p, err := r.newIndexer(indexjob.KindTelegram)
	if err != nil {
		return err
	}

	src := index.SourceTelegram
	if reset {
		if err := resetSource(ctx, db, vectors, src); err != nil {
			return err
		}
	}

	var docs []index.Document
	anyErr := false
	nextCurStr := ""

	if dumpConfigured {
		dumpResult, err := telegramingest.IngestDump(ctx, db, dumpPaths)
		if err != nil {
			lg.Error("telegram dump ingest failed", zap.Error(err))
			anyErr = true
		} else {
			docs = append(docs, dumpResult.Documents...)
			lg.Info("telegram dump ingest complete",
				zap.Int("messages", dumpResult.TotalMessages),
				zap.Int("conversations", dumpResult.TotalConvos))
		}
	}

	if liveConfigured {
		if _, err := os.Stat(tc.IngestSession); err != nil {
			return errors.Wrap(err, "telegram ingest session file not found")
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

		docs = append(docs, result.Documents...)
		if result.NextCursor.PerChat != nil {
			if s, err := result.NextCursor.Encode(); err == nil {
				nextCurStr = s
			}
		}
	}

	batch := docs
	if limit > 0 && limit < len(batch) {
		batch = batch[:limit]
	}
	processed, idxErr := indexBatch(ctx, lg, p, batch, dry, "telegram")
	if idxErr {
		anyErr = true
	}

	now := time.Now()
	st := "ok"
	if anyErr && !dry {
		st = "error"
	}
	count := len(batch)
	if count == 0 {
		count = processed
	}
	_ = upsertSyncState(ctx, db, string(src), now, nextCurStr, st, count)

	return nil
}

func resetSource(ctx context.Context, db *ent.Client, vectors pipeline.VectorStore, src index.Source) error {
	return ingestrun.ResetSource(ctx, db, vectors, src)
}

func upsertSyncState(ctx context.Context, db *ent.Client, src string, lastSynced time.Time, lastCursor, status string, docCount int) error {
	return ingestrun.UpsertSyncState(ctx, db, src, lastSynced, lastCursor, status, docCount)
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
