// Command ingest runs one-shot ingestion for gitlab/jira/telegram sources.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/gotd/log/logzap"
	gotdtelegram "github.com/gotd/td/telegram"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	chunkjira "github.com/go-faster/scpbot/internal/chunk/jira"
	chunkmd "github.com/go-faster/scpbot/internal/chunk/markdown"
	chunktg "github.com/go-faster/scpbot/internal/chunk/telegram"
	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/embed"
	"github.com/go-faster/scpbot/internal/ent"
	"github.com/go-faster/scpbot/internal/ent/chunk"
	"github.com/go-faster/scpbot/internal/ent/document"
	"github.com/go-faster/scpbot/internal/ent/syncstate"
	"github.com/go-faster/scpbot/internal/index"
	gitlabingest "github.com/go-faster/scpbot/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/scpbot/internal/ingest/jira"
	telegramingest "github.com/go-faster/scpbot/internal/ingest/telegram"
	"github.com/go-faster/scpbot/internal/pipeline"
	qdrant "github.com/go-faster/scpbot/internal/search/qdrant"
)

var (
	errNotConfigured = errors.New("source not configured")
	errLimitReached  = errors.New("limit reached")
)

func main() {
	app.Run(func(ctx context.Context, lg *zap.Logger, _ *app.Telemetry) error {
		return run(ctx, lg)
	})
}

func run(ctx context.Context, lg *zap.Logger) error {
	// Parse subcommand + flags (subcommand wins over -source).
	cmd := ""
	flagArgs := os.Args[1:]
	if len(os.Args) > 1 {
		a1 := os.Args[1]
		if !strings.HasPrefix(a1, "-") {
			cmd = a1
			flagArgs = os.Args[2:]
		}
	}

	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	sourceFlag := fs.String("source", "", "alternative to positional subcommand")
	sinceFlag := fs.String("since", "", "RFC3339 override for cursor (jira)")
	resetFlag := fs.String("reset", "none", "reset source|all|none before run")
	yesAll := fs.Bool("yes-i-mean-all", false, "confirm for -reset all")
	limit := fs.Int("limit", 0, "cap documents per source (0=unlimited)")
	dryRun := fs.Bool("dry-run", false, "fetch and log only; skip pipeline.Index")

	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(os.Stderr)
			os.Exit(0)
			return nil
		}
		fmt.Fprintf(os.Stderr, "flag parse error: %v\n", err)
		os.Exit(2)
		return nil
	}

	eff := cmd
	if *sourceFlag != "" {
		if cmd != "" {
			eff = cmd // subcommand wins
		} else {
			eff = *sourceFlag
		}
	}
	if eff == "" {
		printUsage(os.Stderr)
		os.Exit(2)
		return nil
	}

	switch eff {
	case "gitlab", "jira", "telegram", "all":
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", eff)
		printUsage(os.Stderr)
		os.Exit(2)
		return nil
	}

	var since time.Time
	if *sinceFlag != "" {
		var err error
		since, err = time.Parse(time.RFC3339, *sinceFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --since %q: %v\n", *sinceFlag, err)
			os.Exit(2)
			return nil
		}
	}

	if *resetFlag == "all" && !*yesAll {
		fmt.Fprintf(os.Stderr, "refusing --reset all without --yes-i-mean-all\n")
		os.Exit(1)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return errors.Wrap(err, "config")
	}

	// Postgres + ent.
	db, err := sql.Open("pgx", cfg.DatabaseDSN)
	if err != nil {
		return errors.Wrap(err, "open db")
	}
	defer func() { _ = db.Close() }()

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	if err := client.Schema.Create(ctx); err != nil {
		return errors.Wrap(err, "migrate schema")
	}

	// Embedder + optional vector store (match cmd/scpbot pattern).
	embedder, err := embed.New(cfg)
	if err != nil {
		return errors.Wrap(err, "embedder")
	}

	var vectors pipeline.VectorStore
	if host, port, err := splitHostPort(cfg.QdrantAddr); err == nil {
		store, err := qdrant.New(qdrant.Config{
			Host:       host,
			Port:       port,
			Collection: cfg.QdrantCollection,
			Dim:        cfg.EmbedDim,
			Embedder:   embedder,
		})
		if err != nil {
			lg.Warn("qdrant unavailable, vectors disabled", zap.Error(err))
		} else if err := store.EnsureCollection(ctx); err != nil {
			lg.Warn("qdrant collection setup failed, vectors disabled", zap.Error(err))
		} else {
			vectors = store
		}
	}

	switch eff {
	case "gitlab":
		ch := chunkmd.New(chunkmd.ChunkerOptions{})
		pipe := pipeline.New(client, ch, embedder, vectors, lg.Named("pipeline"))
		doReset := *resetFlag == "gitlab" || *resetFlag == "all"
		if err := runGitLab(ctx, lg, client, pipe, vectors, cfg, since, doReset, *limit, *dryRun); err != nil {
			if errors.Is(err, errNotConfigured) {
				fmt.Fprintf(os.Stderr, "gitlab not configured\n")
				os.Exit(1)
				return nil
			}
			return err
		}
		return nil

	case "jira":
		ch := chunkjira.New()
		pipe := pipeline.New(client, ch, embedder, vectors, lg.Named("pipeline"))
		doReset := *resetFlag == "jira" || *resetFlag == "all"
		if err := runJira(ctx, lg, client, pipe, vectors, cfg, since, doReset, *limit, *dryRun); err != nil {
			if errors.Is(err, errNotConfigured) {
				fmt.Fprintf(os.Stderr, "jira not configured\n")
				os.Exit(1)
				return nil
			}
			return err
		}
		return nil

	case "telegram":
		ch := chunktg.New()
		pipe := pipeline.New(client, ch, embedder, vectors, lg.Named("pipeline"))
		doReset := *resetFlag == "telegram" || *resetFlag == "all"
		if err := runTelegram(ctx, lg, client, pipe, vectors, cfg, since, doReset, *limit, *dryRun); err != nil {
			if errors.Is(err, errNotConfigured) {
				fmt.Fprintf(os.Stderr, "telegram not configured or ingest session missing\n")
				os.Exit(1)
				return nil
			}
			return err
		}
		return nil

	case "all":
		return runAll(ctx, lg, client, embedder, vectors, cfg, since, *resetFlag, *yesAll, *limit, *dryRun)
	default:
		printUsage(os.Stderr)
		os.Exit(2)
		return nil
	}
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: ingest <subcommand> [flags]

Subcommands:
  gitlab     ingest GitLab docs
  jira       ingest Jira issues
  telegram   backfill Telegram chats
  all        run gitlab then jira then telegram in sequence

Flags:
  -source string       alternative to positional subcommand (subcommand wins if both)
  -since string        RFC3339 time to override cursor (jira); ignored for telegram/gitlab
  -reset string        reset source data before ingest: gitlab|jira|telegram|all|none (default "none")
  -yes-i-mean-all      required with -reset all
  -limit int           max documents to process per source (0 = unlimited)
  -dry-run             fetch + log what would be indexed; skip pipeline.Index (and vector writes)
  -h, --help
`)
}

func splitHostPort(addr string) (host string, port int, err error) {
	portStr := ""
	host, portStr, err = net.SplitHostPort(addr)
	if err != nil {
		return "", 0, errors.Wrap(err, "split host port")
	}
	p, perr := strconv.Atoi(portStr)
	if perr != nil {
		return "", 0, errors.Wrap(perr, "parse port")
	}
	return host, p, nil
}

func runGitLab(ctx context.Context, lg *zap.Logger, db *ent.Client, p *pipeline.Pipeline, vectors pipeline.VectorStore, cfg config.Config, _ time.Time, reset bool, limit int, dry bool) error {
	roots := gitLabSources(cfg.GitLab.Repos)
	if len(roots) == 0 {
		lg.Info("gitlab not configured")
		return errNotConfigured
	}
	roots, err := gitlabingest.Prepare(ctx, roots, gitlabingest.SyncOptions{
		WorkDir: cfg.GitLab.WorkDir,
		Token:   cfg.GitLab.Token,
		Logger:  lg.Named("gitlab"),
	})
	if err != nil {
		return errors.Wrap(err, "prepare gitlab repos")
	}

	src := index.SourceGitLabDocs
	if reset {
		if err := resetSource(ctx, lg, db, vectors, src); err != nil {
			return err
		}
	}

	docs, err := gitlabingest.WalkAll(ctx, roots, gitlabingest.WalkOptions{Logger: lg.Named("gitlab")})
	if err != nil {
		return errors.Wrap(err, "gitlab walk")
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

func runJira(ctx context.Context, lg *zap.Logger, db *ent.Client, p *pipeline.Pipeline, vectors pipeline.VectorStore, cfg config.Config, since time.Time, reset bool, limit int, dry bool) error {
	jc := cfg.Jira
	if jc.BaseURL == "" || (jc.PAT == "" && (jc.Username == "" || jc.Password == "") && (jc.Email == "" || jc.APIToken == "")) {
		lg.Info("jira not configured")
		return errNotConfigured
	}

	src := index.SourceJira
	if reset {
		if err := resetSource(ctx, lg, db, vectors, src); err != nil {
			return err
		}
	}

	fetcher, err := jiraingest.New(jiraingest.Options{
		BaseURL:  jc.BaseURL,
		Email:    jc.Email,
		Username: jc.Username,
		APIToken: jc.APIToken,
		Password: jc.Password,
		PAT:      jc.PAT,
		Logger:   lg.Named("jira"),
	})
	if err != nil {
		return errors.Wrap(err, "jira new fetcher")
	}

	cur, _ := loadJiraCursor(ctx, db, string(src))
	if !since.IsZero() {
		cur.LastUpdated = since.Format(time.RFC3339)
		cur.StartAt = 0
	}

	projects := splitCSV(jc.Projects)

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
		// SAVE CURSOR AFTER successful (or partial) batch index.
		// If we crash mid-batch we will re-fetch the batch on restart (safe due to body-hash skip in pipeline).
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

func runTelegram(ctx context.Context, lg *zap.Logger, db *ent.Client, p *pipeline.Pipeline, vectors pipeline.VectorStore, cfg config.Config, since time.Time, reset bool, limit int, dry bool) error {
	tc := cfg.Telegram
	if tc.AppID == 0 || tc.AppHash == "" || tc.IngestSession == "" {
		lg.Info("telegram not configured")
		return errNotConfigured
	}
	if _, err := os.Stat(tc.IngestSession); err != nil {
		return errors.Wrap(err, "telegram ingest session file not found")
	}

	src := index.SourceTelegram
	if reset {
		if err := resetSource(ctx, lg, db, vectors, src); err != nil {
			return err
		}
	}

	tgClient := gotdtelegram.NewClient(tc.AppID, tc.AppHash, gotdtelegram.Options{
		Logger:         logzap.New(lg.Named("td").Named("ingest")),
		SessionStorage: &gotdtelegram.FileSessionStorage{Path: tc.IngestSession},
	})

	var result telegramingest.BackfillResult
	var backfillErr error

	runErr := tgClient.Run(ctx, func(ctx context.Context) error {
		bf, err := telegramingest.NewBackfiller(db, telegramingest.BackfillOptions{
			Session: tgClient,
			Logger:  lg.Named("telegram"),
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

	if backfillErr != nil {
		return errors.Wrap(backfillErr, "telegram")
	}
	return nil
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

func runAll(ctx context.Context, lg *zap.Logger, db *ent.Client, embedder index.Embedder, vectors pipeline.VectorStore, cfg config.Config, since time.Time, resetMode string, _ bool, limit int, dry bool) error {
	// Centralized all reset check already done in caller, but if reset all we pre-wipe.
	if resetMode == "all" {
		for _, s := range []index.Source{index.SourceGitLabDocs, index.SourceJira, index.SourceTelegram} {
			if err := resetSource(ctx, lg, db, vectors, s); err != nil {
				return err
			}
		}
	}

	var failed []string

	// gitlab
	{
		ch := chunkmd.New(chunkmd.ChunkerOptions{})
		pipe := pipeline.New(db, ch, embedder, vectors, lg.Named("pipeline"))
		doReset := resetMode == "all" || resetMode == "gitlab"
		if err := runGitLab(ctx, lg, db, pipe, vectors, cfg, since, doReset, limit, dry); err != nil {
			if errors.Is(err, errNotConfigured) {
				lg.Info("skipping gitlab (not configured)")
			} else {
				lg.Error("gitlab failed", zap.Error(err))
				failed = append(failed, "gitlab")
			}
		}
	}

	// jira
	{
		ch := chunkjira.New()
		pipe := pipeline.New(db, ch, embedder, vectors, lg.Named("pipeline"))
		doReset := resetMode == "all" || resetMode == "jira"
		if err := runJira(ctx, lg, db, pipe, vectors, cfg, since, doReset, limit, dry); err != nil {
			if errors.Is(err, errNotConfigured) {
				lg.Info("skipping jira (not configured)")
			} else {
				lg.Error("jira failed", zap.Error(err))
				failed = append(failed, "jira")
			}
		}
	}

	// telegram
	{
		ch := chunktg.New()
		pipe := pipeline.New(db, ch, embedder, vectors, lg.Named("pipeline"))
		doReset := resetMode == "all" || resetMode == "telegram"
		if err := runTelegram(ctx, lg, db, pipe, vectors, cfg, since, doReset, limit, dry); err != nil {
			if errors.Is(err, errNotConfigured) {
				lg.Info("skipping telegram (not configured)")
			} else {
				lg.Error("telegram failed", zap.Error(err))
				failed = append(failed, "telegram")
			}
		}
	}

	if len(failed) > 0 {
		return errors.New("ingest all failed for: " + strings.Join(failed, ","))
	}
	return nil
}

func resetSource(ctx context.Context, lg *zap.Logger, db *ent.Client, vectors pipeline.VectorStore, src index.Source) error {
	// Collect chunk IDs (these are also the qdrant point IDs).
	chunkIDs, err := db.Chunk.Query().
		Where(chunk.HasDocumentWith(document.Source(string(src)))).
		IDs(ctx)
	if err != nil {
		return errors.Wrap(err, "query chunks for reset")
	}

	// Tx: delete chunks then documents then syncstate (no cascade in schema).
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

	// Qdrant delete after successful ent commit. Batch to be safe.
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
