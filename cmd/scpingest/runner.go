package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/ent/chunk"
	"github.com/go-faster/scpbot/internal/ent/document"
	"github.com/go-faster/scpbot/internal/ent/syncstate"
	"github.com/go-faster/scpbot/internal/index"
	gitlabingest "github.com/go-faster/scpbot/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/scpbot/internal/ingest/jira"
	telegramingest "github.com/go-faster/scpbot/internal/ingest/telegram"
	"github.com/go-faster/scpbot/internal/netclient"
	"github.com/go-faster/scpbot/internal/pipeline"
	"github.com/go-faster/scpbot/internal/telemetry"

	"github.com/gotd/log/logzap"
	gotdtelegram "github.com/gotd/td/telegram"
)

var (
	errNotConfigured = errors.New("source not configured")
	errLimitReached  = errors.New("limit reached")
)

type runner struct {
	db      *ent.Client
	vectors pipeline.VectorStore
	cfg     config.Config
	tp      trace.TracerProvider
	mp      metric.MeterProvider
}

func (r *runner) runGitLab(ctx context.Context, p *pipeline.Pipeline, _ time.Time, reset bool, limit int, dry, prune bool) error {
	lg := zctx.From(ctx).Named("gitlab")
	cfg := r.cfg
	db := r.db
	vectors := r.vectors

	roots := gitLabSources(cfg.GitLab.Repos)
	if len(roots) == 0 {
		lg.Info("gitlab not configured, zero sources")
		return errNotConfigured
	}
	roots, err := gitlabingest.Prepare(ctx, roots, gitlabingest.SyncOptions{
		WorkDir: cfg.GitLab.WorkDir,
		Token:   cfg.GitLab.Token,
		Proxy:   cfg.Proxies.GitLab,
	})
	if err != nil {
		return errors.Wrap(err, "prepare gitlab repos")
	}

	src := index.SourceGitLabDocs
	if reset {
		if err := resetSource(ctx, db, vectors, src); err != nil {
			return err
		}
	}

	docs, err := gitlabingest.WalkAll(ctx, roots, gitlabingest.WalkOptions{})
	if err != nil {
		return errors.Wrap(err, "gitlab walk")
	}

	walkedIDs := make([]string, 0, len(docs))
	for _, d := range docs {
		walkedIDs = append(walkedIDs, d.SourceID)
	}

	processed := 0
	anyErr := false
	for _, d := range docs {
		if limit > 0 && processed >= limit {
			break
		}
		if dry {
			lg.Info("dry-run would index gitlab",
				zap.String("source_id", d.SourceID),
				zap.String("title", d.Title))
			processed++
			continue
		}
		if err := p.Index(ctx, d); err != nil {
			lg.Error("index gitlab doc failed", zap.Error(err), zap.String("source_id", d.SourceID))
			anyErr = true
			continue
		}
		processed++
	}

	if !dry && prune && limit <= 0 {
		if err := r.pruneGitLabOrphans(ctx, walkedIDs); err != nil {
			lg.Error("prune gitlab orphans failed", zap.Error(err))
			anyErr = true
		}
	}

	now := time.Now()
	status := "ok"
	if anyErr && !dry {
		status = "error"
	}
	if err := upsertSyncState(ctx, db, string(src), now, "", status, processed); err != nil {
		return errors.Wrap(err, "upsert syncstate")
	}
	if anyErr {
		return errors.New("one or more gitlab documents failed to index")
	}
	return nil
}

func (r *runner) pruneGitLabOrphans(ctx context.Context, walkedSourceIDs []string) error {
	lg := zctx.From(ctx).Named("gitlab.prune")
	db := r.db

	orphanChunkIDs, err := db.Chunk.Query().
		Where(chunk.HasDocumentWith(
			document.Source(string(index.SourceGitLabDocs)),
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
			document.Source(string(index.SourceGitLabDocs)),
			document.SourceIDNotIn(walkedSourceIDs...),
		)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "delete orphan chunks")
	}
	n, err := tx.Document.Delete().
		Where(
			document.Source(string(index.SourceGitLabDocs)),
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

	lg.Info("pruned orphan gitlab documents",
		zap.Int("documents", n),
		zap.Int("chunks", len(orphanChunkIDs)))

	if r.vectors != nil {
		const batch = 1000
		for i := 0; i < len(orphanChunkIDs); i += batch {
			j := min(i+batch, len(orphanChunkIDs))
			if derr := r.vectors.Delete(ctx, orphanChunkIDs[i:j]); derr != nil {
				lg.Warn("qdrant delete for orphan chunks (non-fatal)",
					zap.Error(derr),
					zap.Int("count", j-i))
			}
		}
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

	src := index.SourceJira
	httpClient, err := netclient.HTTPClient(ctx, "jira", cfg.Proxies.Jira, netclient.HTTPClientOptions{
		TracerProvider: r.tp,
		MeterProvider:  r.mp,
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
	projects := splitCSV(jc.Projects)
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
		for _, d := range docs {
			if limit > 0 && processed >= limit {
				return errLimitReached
			}
			if dry {
				lg.Info("dry-run would index jira", zap.String("source_id", d.SourceID), zap.String("title", d.Title))
				processed++
				continue
			}
			if ierr := p.Index(ctx, d); ierr != nil {
				lg.Error("index jira doc failed", zap.Error(ierr), zap.String("source_id", d.SourceID))
				anyErr = true
				continue
			}
			processed++
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
		Middlewares:    []gotdtelegram.Middleware{telemetry.TDTracingMiddleware(r.tp)},
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

		chats := parseMonitorChats(tc.MonitorChats)
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

	processed := 0
	anyErr := false
	for _, d := range result.Documents {
		if dry {
			lg.Info("dry-run would index telegram", zap.String("source_id", d.SourceID))
			processed++
			continue
		}
		if err := p.Index(ctx, d); err != nil {
			lg.Error("index telegram doc failed", zap.Error(err), zap.String("source_id", d.SourceID))
			anyErr = true
			continue
		}
		processed++
	}

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

func gitLabSources(sources []config.GitLabSource) []gitlabingest.Source {
	out := make([]gitlabingest.Source, 0, len(sources))
	for _, src := range sources {
		out = append(out, gitlabingest.Source{
			Root:    src.Root,
			URL:     src.URL,
			Repo:    src.Repo,
			Branch:  src.Branch,
			BaseURL: src.BaseURL,
		})
	}
	return out
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

func parseMonitorChats(s string) []telegramingest.ChatSpec {
	if s == "" {
		return nil
	}
	var out []telegramingest.ChatSpec
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, telegramingest.ChatSpec{ID: id})
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
