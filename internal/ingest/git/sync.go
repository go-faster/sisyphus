package git

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitHTTP "github.com/go-git/go-git/v5/plumbing/transport/http"
	"go.uber.org/zap"
)

var repoMu sync.Mutex

// SyncOptions configures repository clone/fetch before walking docs.
type SyncOptions struct {
	WorkDir string
	Token   string
	Proxy   string
}

// Prepare clones or updates configured repositories and returns the walkable source.
func Prepare(ctx context.Context, src Source, opts SyncOptions) (Source, error) {
	if src.Repo == "" {
		src.Repo = defaultRepoName(src)
	}
	if src.Root == "" && src.URL != "" {
		if opts.WorkDir == "" {
			return src, errors.New("git: work_dir is required for cloned repos")
		}
		src.Root = filepath.Join(opts.WorkDir, safeDirName(src.Repo))
	}
	if src.URL != "" {
		if err := syncRepo(ctx, src, opts); err != nil {
			return src, errors.Wrap(err, "sync git repo")
		}
	}
	return src, nil
}

func syncRepo(ctx context.Context, src Source, opts SyncOptions) error {
	lg := zctx.From(ctx)
	repoMu.Lock()
	defer repoMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(src.Root), 0o750); err != nil {
		return errors.Wrap(err, "create work dir")
	}

	auth := gitAuth(opts.Token)
	if _, err := os.Stat(filepath.Join(src.Root, ".git")); err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrap(err, "stat git dir")
		}
		lg.Info("cloning git repository",
			zap.String("repo", src.Repo),
			zap.String("root", src.Root),
			zap.String("url", redactURL(src.URL)))
		cloneOpts := &git.CloneOptions{
			URL:          src.URL,
			Auth:         auth,
			ProxyOptions: transport.ProxyOptions{URL: opts.Proxy},
		}
		if src.Branch != "" {
			cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(src.Branch)
			cloneOpts.SingleBranch = true
		}
		if _, err := git.PlainCloneContext(ctx, src.Root, false, cloneOpts); err != nil {
			return errors.Wrap(err, "clone")
		}
		return nil
	}

	repo, err := git.PlainOpen(src.Root)
	if err != nil {
		return errors.Wrap(err, "open repository")
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return errors.Wrap(err, "worktree")
	}

	lg.Info("updating git repository",
		zap.String("repo", src.Repo),
		zap.String("root", src.Root),
		zap.String("url", redactURL(src.URL)))
	pullOpts := &git.PullOptions{
		RemoteName:   "origin",
		Auth:         auth,
		ProxyOptions: transport.ProxyOptions{URL: opts.Proxy},
	}
	if src.Branch != "" {
		pullOpts.ReferenceName = plumbing.NewBranchReferenceName(src.Branch)
		pullOpts.SingleBranch = true
	}
	if err := worktree.PullContext(ctx, pullOpts); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return errors.Wrap(err, "pull")
	}
	return nil
}

func gitAuth(token string) transport.AuthMethod {
	if token == "" {
		return nil
	}
	return &gitHTTP.BasicAuth{
		Username: "oauth2",
		Password: token,
	}
}

func defaultRepoName(src Source) string {
	if src.Root != "" {
		return filepath.Base(src.Root)
	}
	if src.URL == "" {
		return ""
	}
	u, err := url.Parse(src.URL)
	if err != nil {
		return strings.TrimSuffix(filepath.Base(src.URL), ".git")
	}
	return strings.TrimSuffix(filepath.Base(u.Path), ".git")
}

func safeDirName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "repo"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}
