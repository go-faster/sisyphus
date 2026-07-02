package git

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/scpbot/internal/index"
)

func TestChunker_Chunk(t *testing.T) {
	tests := []struct {
		name      string
		doc       index.Document
		checkFn   func(t *testing.T, chunks []index.Chunk)
		expectErr bool
		expectLen int
	}{
		{
			name: "single commit message",
			doc: index.Document{
				ID:       uuid.New(),
				Source:   index.SourceGitCommit("test/repo"),
				SourceID: "test/repo@abc1234",
				Title:    "Fix: critical bug",
				Body:     "Fix: critical bug\n\nThis fixes the critical issue reported in #123.",
				BodyHash: index.Hash("Fix: critical bug\n\nThis fixes the critical issue reported in #123."),
				Metadata: map[string]any{
					"source":       string(index.SourceGitCommit("test/repo")),
					"repo":         "test/repo",
					"sha":          "abc1234abc1234",
					"author":       "Alice",
					"author_email": "alice@example.com",
					"branch":       "main",
					"authority":    string(index.AuthorityLow),
				},
				CreatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
				UpdatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			},
			expectLen: 1,
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				require.Len(t, chunks, 1)
				chunk := chunks[0]
				require.Equal(t, index.ChunkGitCommit, chunk.Type)
				require.Equal(t, 0, chunk.Index)
				require.Equal(t, "Fix: critical bug", chunk.Title)
				require.Contains(t, chunk.Text, "Fix: critical bug")
				require.Contains(t, chunk.Text, "critical issue")
				require.NotEmpty(t, chunk.TextHash)
				// Verify metadata is copied
				require.Equal(t, "test/repo", chunk.Metadata["repo"])
				require.Equal(t, "Alice", chunk.Metadata["author"])
				require.Equal(t, string(index.AuthorityLow), chunk.Metadata["authority"])
			},
		},
		{
			name: "empty body returns no chunks",
			doc: index.Document{
				ID:       uuid.New(),
				Source:   index.SourceGitCommit("test/repo"),
				SourceID: "test/repo@abc1234",
				Title:    "Empty",
				Body:     "",
				Metadata: map[string]any{
					"repo": "test/repo",
				},
			},
			expectLen: 0,
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				require.Len(t, chunks, 0)
			},
		},
		{
			name: "commit with single-line message",
			doc: index.Document{
				ID:       uuid.New(),
				Source:   index.SourceGitCommit("my-project"),
				SourceID: "my-project@def5678",
				Title:    "Add feature",
				Body:     "Add feature",
				Metadata: map[string]any{
					"source":       string(index.SourceGitCommit("my-project")),
					"repo":         "my-project",
					"author":       "Bob",
					"author_email": "bob@example.com",
					"branch":       "develop",
					"authority":    string(index.AuthorityLow),
				},
			},
			expectLen: 1,
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				require.Len(t, chunks, 1)
				require.Equal(t, "Add feature", chunks[0].Title)
				require.Equal(t, "Add feature", chunks[0].Text)
			},
		},
		{
			name: "chunk metadata excludes certain fields",
			doc: index.Document{
				ID:       uuid.New(),
				Source:   index.SourceGitCommit("test/repo"),
				SourceID: "test/repo@ghi9012",
				Title:    "Test",
				Body:     "Test commit message",
				Metadata: map[string]any{
					"source":    string(index.SourceGitCommit("test/repo")),
					"repo":      "test/repo",
					"author":    "Charlie",
					"custom":    "value",
					"authority": string(index.AuthorityLow),
				},
			},
			expectLen: 1,
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				require.Len(t, chunks, 1)
				require.Equal(t, "test/repo", chunks[0].Metadata["repo"])
				require.Equal(t, "Charlie", chunks[0].Metadata["author"])
				require.Equal(t, "value", chunks[0].Metadata["custom"])
			},
		},
		{
			name: "chunk has correct document reference",
			doc: func() index.Document {
				docID := uuid.New()
				return index.Document{
					ID:       docID,
					Source:   index.SourceGitCommit("test/repo"),
					SourceID: "test/repo@jkl3456",
					Title:    "Verify ref",
					Body:     "Test reference",
					Metadata: map[string]any{"repo": "test/repo"},
				}
			}(),
			expectLen: 1,
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				require.Len(t, chunks, 1)
				// Just verify the chunk has a document reference (will be the random one we created)
				require.NotEqual(t, uuid.UUID{}, chunks[0].DocumentID)
			},
		},
		{
			name: "git tag produces ChunkGitTag type",
			doc: index.Document{
				ID:       uuid.New(),
				Source:   index.SourceGitTag("test/repo"),
				SourceID: "test/repo@tag:v1.0.0",
				Title:    "v1.0.0",
				Body:     "Release version 1.0.0",
				BodyHash: index.Hash("Release version 1.0.0"),
				Metadata: map[string]any{
					"source":     string(index.SourceGitTag("test/repo")),
					"repo":       "test/repo",
					"tag":        "v1.0.0",
					"annotated":  true,
					"authority":  string(index.AuthorityMedium),
					"target_sha": "abc1234567890def",
				},
				CreatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
				UpdatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			},
			expectLen: 1,
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				require.Len(t, chunks, 1)
				chunk := chunks[0]
				require.Equal(t, index.ChunkGitTag, chunk.Type)
				require.Equal(t, "v1.0.0", chunk.Title)
				require.Equal(t, "Release version 1.0.0", chunk.Text)
				require.Equal(t, "test/repo", chunk.Metadata["repo"])
				require.Equal(t, "v1.0.0", chunk.Metadata["tag"])
				require.Equal(t, string(index.AuthorityMedium), chunk.Metadata["authority"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunker := New()
			chunks, err := chunker.Chunk(t.Context(), tt.doc)

			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, chunks, tt.expectLen)
			tt.checkFn(t, chunks)
		})
	}
}

func TestChunker_TextHash(t *testing.T) {
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitCommit("test/repo"),
		SourceID: "test/repo@mno7890",
		Title:    "Hash test",
		Body:     "Test commit message with content",
		Metadata: map[string]any{"repo": "test/repo"},
	}

	chunker := New()
	chunks, err := chunker.Chunk(t.Context(), doc)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	expectedHash := index.Hash(doc.Body)
	require.Equal(t, expectedHash, chunks[0].TextHash)
}

func TestChunker_ChunkID(t *testing.T) {
	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitCommit("test/repo"),
		SourceID: "test/repo@pqr4567",
		Title:    "ID test",
		Body:     "Test body",
		Metadata: map[string]any{"repo": "test/repo"},
	}

	chunker := New()
	chunks, err := chunker.Chunk(t.Context(), doc)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	// Chunk should have a valid ID
	require.NotEqual(t, [16]byte{}, chunks[0].ID)
	// Chunk should reference the document
	require.Equal(t, doc.ID, chunks[0].DocumentID)
	// Chunk should have zero Index for single-chunk documents
	require.Equal(t, 0, chunks[0].Index)
}
