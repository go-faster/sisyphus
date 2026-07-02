package git

import (
	"context"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/scpbot/internal/index"
)

func TestWalkTags(t *testing.T) {
	t.Run("walks annotated and lightweight tags", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		// Create an initial commit
		commitNormal(t, repo, "Initial commit", "alice", "alice@example.com")

		head, err := repo.Head()
		require.NoError(t, err)

		// Create an annotated tag
		annotatedTime := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
		tagRef, err := repo.CreateTag("v1.0.0", head.Hash(), &git.CreateTagOptions{
			Tagger: &object.Signature{
				Name:  "Alice",
				Email: "alice@example.com",
				When:  annotatedTime,
			},
			Message: "Release version 1.0.0\n\nThis is the first stable release.",
		})
		require.NoError(t, err)
		require.NotEmpty(t, tagRef)

		// Create a lightweight tag (just a reference without message)
		err = repo.Storer.SetReference(plumbing.NewHashReference(
			plumbing.ReferenceName("refs/tags/v0.9.0"),
			head.Hash(),
		))
		require.NoError(t, err)

		// Walk tags
		result, err := WalkTags(context.Background(), Source{
			Root: root,
			Repo: "test/repo",
		})

		require.NoError(t, err)
		require.Len(t, result, 2)

		// Map results by tag name for easier assertion
		tagsByName := make(map[string]index.Document)
		for _, doc := range result {
			tagsByName[doc.Title] = doc
		}

		// Check annotated tag
		require.Contains(t, tagsByName, "v1.0.0")
		annDoc := tagsByName["v1.0.0"]
		require.Equal(t, index.SourceGitTag("test/repo"), annDoc.Source)
		require.Equal(t, "test/repo@tag:v1.0.0", annDoc.SourceID)
		require.Contains(t, annDoc.Body, "Release version 1.0.0")
		require.Contains(t, annDoc.Body, "first stable release")
		require.Equal(t, "medium", annDoc.Metadata["authority"])
		require.Equal(t, true, annDoc.Metadata["annotated"])
		require.NotEmpty(t, annDoc.Metadata["target_sha"])

		// Check lightweight tag
		require.Contains(t, tagsByName, "v0.9.0")
		lightDoc := tagsByName["v0.9.0"]
		require.Equal(t, index.SourceGitTag("test/repo"), lightDoc.Source)
		require.Equal(t, "test/repo@tag:v0.9.0", lightDoc.SourceID)
		require.Contains(t, lightDoc.Body, "Lightweight tag pointing to")
		require.Contains(t, lightDoc.Body, "Initial commit")
		require.Equal(t, "medium", lightDoc.Metadata["authority"])
		require.Equal(t, false, lightDoc.Metadata["annotated"])
		require.NotEmpty(t, lightDoc.Metadata["target_sha"])
	})

	t.Run("empty repo produces no tags", func(t *testing.T) {
		_, root := newTestGitRepo(t)

		result, err := WalkTags(context.Background(), Source{
			Root: root,
			Repo: "empty/repo",
		})

		require.NoError(t, err)
		require.Len(t, result, 0)
	})

	t.Run("document structure is correct", func(t *testing.T) {
		repo, root := newTestGitRepo(t)

		// Create a commit
		commitNormal(t, repo, "Test commit", "bob", "bob@example.com")

		head, err := repo.Head()
		require.NoError(t, err)

		// Create a tag
		tagTime := time.Date(2024, 2, 20, 15, 45, 30, 0, time.UTC)
		_, err = repo.CreateTag("v2.0.0", head.Hash(), &git.CreateTagOptions{
			Tagger: &object.Signature{
				Name:  "Bob",
				Email: "bob@example.com",
				When:  tagTime,
			},
			Message: "Version 2.0.0",
		})
		require.NoError(t, err)

		result, err := WalkTags(context.Background(), Source{
			Root: root,
			Repo: "my-project",
		})

		require.NoError(t, err)
		require.Len(t, result, 1)

		doc := result[0]
		require.Equal(t, "v2.0.0", doc.Title)
		require.Equal(t, index.SourceGitTag("my-project"), doc.Source)
		require.Equal(t, "my-project@tag:v2.0.0", doc.SourceID)
		require.Equal(t, "Version 2.0.0", doc.Body)
		require.NotEmpty(t, doc.ID)
		require.NotEmpty(t, doc.BodyHash)
		require.True(t, tagTime.Equal(doc.CreatedAt), "CreatedAt: expected %v, got %v", tagTime, doc.CreatedAt)
		require.True(t, tagTime.Equal(doc.UpdatedAt), "UpdatedAt: expected %v, got %v", tagTime, doc.UpdatedAt)
		require.Equal(t, "my-project", doc.Metadata["repo"])
		require.Equal(t, "v2.0.0", doc.Metadata["tag"])
		require.Equal(t, string(index.AuthorityMedium), doc.Metadata["authority"])
	})
}
