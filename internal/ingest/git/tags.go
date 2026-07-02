package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// WalkTags walks all git tags in the repository and returns normalized Documents.
// For each tag ref, it attempts to resolve it as an annotated tag, falling back
// to a lightweight tag (commit object). Errors resolving individual tags are
// logged and skipped; they do not fail the entire walk.
func WalkTags(ctx context.Context, s Source) ([]index.Document, error) {
	lg := zctx.From(ctx)

	repo, err := git.PlainOpen(s.Root)
	if err != nil {
		return nil, errors.Wrap(err, "open repository")
	}

	iter, err := repo.Tags()
	if err != nil {
		return nil, errors.Wrap(err, "list tags")
	}
	defer iter.Close()

	var docs []index.Document

	err = iter.ForEach(func(ref *plumbing.Reference) error {
		tagName := ref.Name().Short()
		targetSHA := ref.Hash().String()
		shortSHA := targetSHA[:12]

		// Try to resolve as annotated tag
		tagObj, err := repo.TagObject(ref.Hash())
		if err != nil && errors.Is(err, plumbing.ErrObjectNotFound) {
			// Lightweight tag: resolve as a commit
			commit, cerr := repo.CommitObject(ref.Hash())
			if cerr != nil {
				lg.Warn("failed to resolve tag commit",
					zap.String("repo", s.Repo),
					zap.String("tag", tagName),
					zap.Error(cerr))
				return nil
			}

			subject, _, _ := strings.Cut(commit.Message, "\n")
			subject = strings.TrimSpace(subject)

			body := fmt.Sprintf("Lightweight tag pointing to %s: %s", shortSHA, subject)
			source := index.SourceGitTag(s.Repo)
			meta := map[string]any{
				"source":     string(source),
				"repo":       s.Repo,
				"tag":        tagName,
				"target_sha": targetSHA,
				"annotated":  false,
				"authority":  string(index.AuthorityMedium),
			}

			doc := index.Document{
				ID:        index.NewID(),
				Source:    source,
				SourceID:  s.Repo + "@tag:" + tagName,
				Title:     tagName,
				Body:      body,
				BodyHash:  index.Hash(body),
				Metadata:  meta,
				CreatedAt: commit.Author.When,
				UpdatedAt: commit.Author.When,
			}
			docs = append(docs, doc)
			return nil
		} else if err != nil {
			// Any other error: log and skip this tag
			lg.Warn("failed to resolve tag object",
				zap.String("repo", s.Repo),
				zap.String("tag", tagName),
				zap.Error(err))
			return nil
		}

		// Annotated tag
		source := index.SourceGitTag(s.Repo)
		meta := map[string]any{
			"source":     string(source),
			"repo":       s.Repo,
			"tag":        tagName,
			"target_sha": targetSHA,
			"annotated":  true,
			"authority":  string(index.AuthorityMedium),
		}

		body := strings.TrimSpace(tagObj.Message)
		doc := index.Document{
			ID:        index.NewID(),
			Source:    source,
			SourceID:  s.Repo + "@tag:" + tagName,
			Title:     tagName,
			Body:      body,
			BodyHash:  index.Hash(body),
			Metadata:  meta,
			CreatedAt: tagObj.Tagger.When,
			UpdatedAt: tagObj.Tagger.When,
		}
		docs = append(docs, doc)
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "walk tags")
	}

	lg.Info("walked tags",
		zap.String("repo", s.Repo),
		zap.Int("count", len(docs)))

	return docs, nil
}
