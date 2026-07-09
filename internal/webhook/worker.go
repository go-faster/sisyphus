package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-faster/errors"
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
	"github.com/go-faster/sisyphus/internal/ent/syncstate"
	"github.com/go-faster/sisyphus/internal/index"
	gitlabingest "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	jiraingest "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/netclient"
	"github.com/go-faster/sisyphus/internal/pipeline"
)

var errNotConfigured = errors.New("source not configured")

const indexConcurrency = 8

// Worker runs provider ingestion triggered by webhooks.
type Worker struct {
	db       *ent.Client
	vectors  pipeline.VectorStore
	embedder index.Embedder
	cfg      config.Config
	tp       trace.TracerProvider
	mp       metric.MeterProvider
	lg       *zap.Logger
}

// WorkerOptions configures the ingestion worker.
type WorkerOptions struct {
	Logger         *zap.Logger
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (opts *WorkerOptions) setDefaults() {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}

// NewWorker creates a webhook-triggered ingestion worker.
func NewWorker(db *ent.Client, vectors pipeline.VectorStore, embedder index.Embedder, cfg config.Config, opts WorkerOptions) *Worker {
	opts.setDefaults()
	return &Worker{
		db:       db,
		vectors:  vectors,
		embedder: embedder,
		cfg:      cfg,
		tp:       opts.TracerProvider,
		mp:       opts.MeterProvider,
		lg:       opts.Logger.Named("webhook_worker"),
	}
}

// RunGitLab runs incremental GitLab ingestion for all enabled resources.
func (w *Worker) RunGitLab(ctx context.Context) error {
	return w.runGitLabAPI(ctx)
}

// RunJira runs incremental Jira ingestion.
func (w *Worker) RunJira(ctx context.Context) error {
	return w.runJira(ctx)
}

func (w *Worker) runGitLabAPI(ctx context.Context) error {
	lg := w.lg.Named("gitlab")
	cfg := w.cfg

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
		TracerProvider: w.tp,
		MeterProvider:  w.mp,
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

	pipe, err := w.pipeline(chunkgitlab.New())
	if err != nil {
		return errors.Wrap(err, "build gitlab pipeline")
	}

	ingestResource := func(resourceName string, enabled bool, src index.Source, fetch func(context.Context, int, gitlabingest.Cursor) (gitlabingest.FetchResult, error)) error {
		if !enabled {
			return nil
		}

		startCur, _ := loadGitLabCursor(ctx, w.db, string(src))
		lg := lg.WithLazy(
			zap.String("src", string(src)),
			zap.String("resource", resourceName),
		)

		processed := 0
		page := 1
		maxObserved := startCur.UpdatedAfter
		var anyErr bool

		for {
			res, err := fetch(ctx, page, startCur)
			if err != nil {
				lg.Error("gitlab fetch failed", zap.Error(err))
				anyErr = true
				break
			}

			n, errFound := indexBatch(ctx, lg, pipe, res.Documents, "gitlab "+resourceName)
			processed += n
			if errFound {
				anyErr = true
			}

			if res.NextCursor.UpdatedAfter > maxObserved {
				maxObserved = res.NextCursor.UpdatedAfter
			}
			if !res.HasMore {
				break
			}
			page++
		}

		curStr, _ := json.Marshal(gitlabingest.Cursor{UpdatedAfter: maxObserved})
		status := "ok"
		if anyErr {
			status = "error"
		}
		if err := upsertSyncState(ctx, w.db, string(src), string(curStr), status, processed); err != nil {
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
	); err != nil && !errors.Is(err, errNotConfigured) {
		return err
	}

	if err := ingestResource("merge_requests", cfg.GitLab.MergeRequests, index.SourceGitLabMR,
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchMergeRequests(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, errNotConfigured) {
		return err
	}

	if err := ingestResource("releases", cfg.GitLab.Releases, index.SourceGitLabRelease,
		func(ctx context.Context, page int, cur gitlabingest.Cursor) (gitlabingest.FetchResult, error) {
			return fetcher.FetchReleases(ctx, page, cur)
		},
	); err != nil && !errors.Is(err, errNotConfigured) {
		return err
	}

	return nil
}

func (w *Worker) runJira(ctx context.Context) error {
	lg := w.lg.Named("jira")
	cfg := w.cfg

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
		TracerProvider: w.tp,
		MeterProvider:  w.mp,
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

	pipe, err := w.pipeline(chunkjira.New())
	if err != nil {
		return errors.Wrap(err, "build jira pipeline")
	}

	cur, _ := loadJiraCursor(ctx, w.db, string(src))

	processed := 0
	finalCur := cur
	var anyErr bool

	_, fetchErr := fetcher.FetchAll(ctx, jiraingest.FetchOptions{
		Projects: projects,
		PageSize: 100,
	}, cur, func(ctx context.Context, docs []index.Document, nextCur jiraingest.Cursor) error {
		n, errFound := indexBatch(ctx, lg, pipe, docs, "jira")
		processed += n
		if errFound {
			anyErr = true
		}
		curStr, _ := json.Marshal(nextCur)
		st := "ok"
		if anyErr {
			st = "error"
		}
		_ = upsertSyncState(ctx, w.db, string(src), string(curStr), st, processed)
		finalCur = nextCur
		return nil
	})

	if fetchErr != nil {
		anyErr = true
	}

	curStr, _ := json.Marshal(finalCur)
	st := "ok"
	if anyErr {
		st = "error"
	}
	_ = upsertSyncState(ctx, w.db, string(src), string(curStr), st, processed)

	if fetchErr != nil {
		return errors.Wrap(fetchErr, "jira fetchall")
	}
	return nil
}

func (w *Worker) pipeline(ch index.Chunker) (*pipeline.Pipeline, error) {
	return pipeline.New(w.db, ch, w.embedder, w.vectors, pipeline.PipelineOptions{
		TracerProvider: w.tp,
		MeterProvider:  w.mp,
	})
}

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

	return diskcache.NewWithDiskv(diskv.New(diskv.Options{
		BasePath:     cachePath,
		CacheSizeMax: 100 * 1024 * 1024,
		PathPerm:     0o700,
		FilePerm:     0o600,
	})), nil
}

func indexBatch(ctx context.Context, lg *zap.Logger, p *pipeline.Pipeline, docs []index.Document, label string) (int, bool) {
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

func upsertSyncState(ctx context.Context, db *ent.Client, src, lastCursor, status string, docCount int) error {
	return db.SyncState.Create().
		SetSource(src).
		SetLastSyncedAt(time.Now()).
		SetLastCursor(lastCursor).
		SetStatus(status).
		SetDocumentCount(docCount).
		OnConflictColumns("source").
		UpdateNewValues().
		Exec(ctx)
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
