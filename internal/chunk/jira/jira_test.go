package jira

import (
	"strings"
	"testing"
	"time"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestDocumentFromIssue(t *testing.T) {
	tests := []struct {
		name    string
		issue   Issue
		checkFn func(t *testing.T, doc index.Document)
	}{
		{
			name: "basic issue",
			issue: Issue{
				Key:         "PROJ-123",
				Title:       "Fix login bug",
				Description: "Users cannot login on mobile",
				Status:      "In Progress",
				Resolution:  "",
				Components:  []string{"auth", "mobile"},
				Labels:      []string{"bug", "urgent"},
				Assignee:    "alice",
				Reporter:    "bob",
				Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated:     time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC),
				Resolved:    time.Time{},
				Comments:    nil,
			},
			checkFn: func(t *testing.T, doc index.Document) {
				if doc.Source != index.SourceJira {
					t.Errorf("expected source %q, got %q", index.SourceJira, doc.Source)
				}
				if doc.SourceID != "PROJ-123" {
					t.Errorf("expected SourceID %q, got %q", "PROJ-123", doc.SourceID)
				}
				if doc.Title != "Fix login bug" {
					t.Errorf("expected title %q, got %q", "Fix login bug", doc.Title)
				}
				if project, ok := doc.Metadata["project"].(string); !ok || project != "PROJ" {
					t.Errorf("expected project %q, got %v", "PROJ", doc.Metadata["project"])
				}
				if key, ok := doc.Metadata["jira_key"].(string); !ok || key != "PROJ-123" {
					t.Errorf("expected jira_key %q, got %v", "PROJ-123", doc.Metadata["jira_key"])
				}
				if _, ok := doc.Metadata["jira_issue"]; !ok {
					t.Errorf("expected jira_issue in metadata")
				}
				if doc.BodyHash == "" {
					t.Errorf("expected BodyHash to be set")
				}
				if doc.Body == "" {
					t.Errorf("expected Body to be non-empty")
				}
			},
		},
		{
			name: "issue with resolved time",
			issue: Issue{
				Key:         "PROJ-456",
				Title:       "Feature complete",
				Description: "Feature is ready",
				Status:      "Done",
				Resolution:  "Fixed",
				Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated:     time.Date(2024, 1, 5, 10, 0, 0, 0, time.UTC),
				Resolved:    time.Date(2024, 1, 5, 15, 0, 0, 0, time.UTC),
			},
			checkFn: func(t *testing.T, doc index.Document) {
				if !strings.Contains(doc.Body, "Resolved:") {
					t.Errorf("expected Body to contain 'Resolved:' timestamp")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := DocumentFromIssue(tt.issue)
			tt.checkFn(t, doc)
		})
	}
}

func TestChunker_Chunk(t *testing.T) {
	tests := []struct {
		name      string
		issue     Issue
		checkFn   func(t *testing.T, chunks []index.Chunk)
		expectErr bool
	}{
		{
			name: "summary only",
			issue: Issue{
				Key:         "PROJ-1",
				Title:       "Summary test",
				Status:      "Open",
				Resolution:  "",
				Description: "",
				Components:  []string{"core"},
				Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
			},
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				if len(chunks) != 1 {
					t.Errorf("expected 1 chunk (summary only), got %d", len(chunks))
					return
				}
				if chunks[0].Type != index.ChunkJiraSummary {
					t.Errorf("expected ChunkJiraSummary, got %q", chunks[0].Type)
				}
				if chunks[0].Index != 0 {
					t.Errorf("expected index 0, got %d", chunks[0].Index)
				}
				if !strings.Contains(chunks[0].Text, "PROJ-1") {
					t.Errorf("expected summary to contain issue key")
				}
			},
		},
		{
			name: "summary and description",
			issue: Issue{
				Key:         "PROJ-2",
				Title:       "With description",
				Status:      "In Progress",
				Description: "This is the description content",
				Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
			},
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				if len(chunks) != 2 {
					t.Errorf("expected 2 chunks, got %d", len(chunks))
					return
				}
				if chunks[0].Type != index.ChunkJiraSummary {
					t.Errorf("expected first chunk to be ChunkJiraSummary, got %q", chunks[0].Type)
				}
				if chunks[1].Type != index.ChunkJiraDescription {
					t.Errorf("expected second chunk to be ChunkJiraDescription, got %q", chunks[1].Type)
				}
				if chunks[1].Text != "This is the description content" {
					t.Errorf("expected description text, got %q", chunks[1].Text)
				}
			},
		},
		{
			name: "all chunk types",
			issue: Issue{
				Key:         "PROJ-3",
				Title:       "All types",
				Status:      "Done",
				Description: "Description here",
				Resolution:  "Fixed",
				Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
			},
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				if len(chunks) != 3 {
					t.Errorf("expected 3 chunks (summary, description, resolution), got %d", len(chunks))
					return
				}
				if chunks[0].Type != index.ChunkJiraSummary {
					t.Errorf("expected chunk 0 to be ChunkJiraSummary")
				}
				if chunks[1].Type != index.ChunkJiraDescription {
					t.Errorf("expected chunk 1 to be ChunkJiraDescription")
				}
				if chunks[2].Type != index.ChunkJiraResolution {
					t.Errorf("expected chunk 2 to be ChunkJiraResolution")
				}
				if chunks[2].Text != "Fixed" {
					t.Errorf("expected resolution text %q, got %q", "Fixed", chunks[2].Text)
				}
			},
		},
		{
			name: "comments grouping",
			issue: Issue{
				Key:         "PROJ-4",
				Title:       "With comments",
				Status:      "Open",
				Description: "",
				Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Comments: []Comment{
					{
						Author:  "alice",
						Body:    "First comment",
						Created: time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC),
					},
					{
						Author:  "bob",
						Body:    "ok",
						Created: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
					},
					{
						Author:  "charlie",
						Body:    "Third comment is substantive",
						Created: time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC),
					},
				},
			},
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				// summary + 1 comment group (trivial "ok" filtered out)
				if len(chunks) != 2 {
					t.Errorf("expected 2 chunks (summary + 1 comment group), got %d", len(chunks))
					return
				}
				if chunks[1].Type != index.ChunkJiraComments {
					t.Errorf("expected chunk 1 to be ChunkJiraComments, got %q", chunks[1].Type)
				}
				// Should contain first and third comments, not the "ok"
				if !strings.Contains(chunks[1].Text, "alice") {
					t.Errorf("expected alice's comment")
				}
				if !strings.Contains(chunks[1].Text, "Third comment") {
					t.Errorf("expected third comment")
				}
				if strings.Contains(chunks[1].Text, "ok") {
					t.Errorf("trivial comment should be filtered out")
				}
			},
		},
		{
			name: "comments multiple groups",
			issue: Issue{
				Key:     "PROJ-5",
				Title:   "Many comments",
				Status:  "Open",
				Created: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Comments: func() []Comment {
					var comments []Comment
					// Create 10 substantive comments to trigger grouping
					for i := range 10 {
						comments = append(comments, Comment{
							Author:  "user" + string(rune('0'+i)),
							Body:    "Comment " + string(rune('0'+i)),
							Created: time.Date(2024, 1, 1, 11+i, 0, 0, 0, time.UTC),
						})
					}
					return comments
				}(),
			},
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				// summary + 2 comment groups (8 + 2)
				if len(chunks) != 3 {
					t.Errorf("expected 3 chunks (summary + 2 groups), got %d", len(chunks))
					return
				}
				if chunks[1].Type != index.ChunkJiraComments || chunks[2].Type != index.ChunkJiraComments {
					t.Errorf("expected chunks 1 and 2 to be comment groups")
				}
			},
		},
		{
			name: "all trivial comments filtered",
			issue: Issue{
				Key:     "PROJ-6",
				Title:   "All trivial",
				Status:  "Open",
				Created: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Updated: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
				Comments: []Comment{
					{Author: "a", Body: "ok", Created: time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC)},
					{Author: "b", Body: "done", Created: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)},
					{Author: "c", Body: "+1", Created: time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC)},
				},
			},
			checkFn: func(t *testing.T, chunks []index.Chunk) {
				// Only summary, no comment chunks
				if len(chunks) != 1 {
					t.Errorf("expected 1 chunk (summary only, all comments filtered), got %d", len(chunks))
					return
				}
				if chunks[0].Type != index.ChunkJiraSummary {
					t.Errorf("expected ChunkJiraSummary")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := DocumentFromIssue(tt.issue)
			chunker := New()

			chunks, err := chunker.Chunk(t.Context(), doc)
			if (err != nil) != tt.expectErr {
				t.Errorf("unexpected error: %v", err)
			}

			tt.checkFn(t, chunks)
		})
	}
}

func TestChunk_Metadata(t *testing.T) {
	// Verify that chunk metadata does not include the raw jira_issue
	issue := Issue{
		Key:         "PROJ-7",
		Title:       "Metadata test",
		Status:      "Open",
		Description: "Test description",
		Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		Updated:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
	}

	doc := DocumentFromIssue(issue)
	chunker := New()

	chunks, err := chunker.Chunk(t.Context(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, chunk := range chunks {
		if _, ok := chunk.Metadata["jira_issue"]; ok {
			t.Errorf("chunk metadata should not contain jira_issue")
		}
		if jiraKey, ok := chunk.Metadata["jira_key"].(string); !ok || jiraKey != "PROJ-7" {
			t.Errorf("expected jira_key in chunk metadata, got %v", chunk.Metadata["jira_key"])
		}
		if project, ok := chunk.Metadata["project"].(string); !ok || project != "PROJ" {
			t.Errorf("expected project in chunk metadata, got %v", chunk.Metadata["project"])
		}
	}
}

func TestChunk_ID_and_Index(t *testing.T) {
	// Verify that each chunk has unique ID and sequential Index
	issue := Issue{
		Key:         "PROJ-8",
		Title:       "ID and Index test",
		Status:      "Open",
		Description: "Description",
		Resolution:  "Fixed",
		Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		Updated:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		Comments: []Comment{
			{Author: "a", Body: "Comment 1", Created: time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC)},
			{Author: "b", Body: "Comment 2", Created: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)},
		},
	}

	doc := DocumentFromIssue(issue)
	chunker := New()

	chunks, err := chunker.Chunk(t.Context(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify sequential Index
	for i, chunk := range chunks {
		if chunk.Index != i {
			t.Errorf("chunk %d: expected Index %d, got %d", i, i, chunk.Index)
		}
		if chunk.DocumentID != doc.ID {
			t.Errorf("chunk %d: expected DocumentID %v, got %v", i, doc.ID, chunk.DocumentID)
		}
		if chunk.ID == [16]byte{} { // zero UUID
			t.Errorf("chunk %d: expected non-zero ID", i)
		}
		if chunk.TextHash == "" {
			t.Errorf("chunk %d: expected non-empty TextHash", i)
		}
	}
}

func TestChunk_TextHash(t *testing.T) {
	// Verify TextHash is correctly computed
	issue := Issue{
		Key:         "PROJ-9",
		Title:       "Hash test",
		Status:      "Open",
		Description: "Test description content",
		Created:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		Updated:     time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
	}

	doc := DocumentFromIssue(issue)
	chunker := New()

	chunks, err := chunker.Chunk(t.Context(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	// Verify description chunk hash
	descChunk := chunks[1]
	if descChunk.Type != index.ChunkJiraDescription {
		t.Fatalf("expected description chunk")
	}

	expectedHash := index.Hash(descChunk.Text)
	if descChunk.TextHash != expectedHash {
		t.Errorf("expected hash %q, got %q", expectedHash, descChunk.TextHash)
	}
}

func TestFallback_NoStructuredIssue(t *testing.T) {
	// Test fallback when structured Issue is not in metadata
	doc := index.Document{
		ID:        [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		Source:    index.SourceJira,
		SourceID:  "PROJ-99",
		Title:     "Fallback test",
		Body:      "Some body text",
		Metadata:  map[string]any{"jira_key": "PROJ-99"},
		CreatedAt: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
	}

	chunker := New()
	chunks, err := chunker.Chunk(t.Context(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
		return
	}

	if chunks[0].Type != index.ChunkJiraDescription {
		t.Errorf("expected ChunkJiraDescription, got %q", chunks[0].Type)
	}
	if chunks[0].Text != "Some body text" {
		t.Errorf("expected body text, got %q", chunks[0].Text)
	}
}

func TestFallback_EmptyBody(t *testing.T) {
	// Test fallback with empty body
	doc := index.Document{
		ID:        [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 17},
		Source:    index.SourceJira,
		SourceID:  "PROJ-100",
		Title:     "Empty body test",
		Body:      "",
		Metadata:  map[string]any{},
		CreatedAt: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
	}

	chunker := New()
	chunks, err := chunker.Chunk(t.Context(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty body, got %d", len(chunks))
	}
}
