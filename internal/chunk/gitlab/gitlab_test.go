package gitlab

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestDocumentFromIssue(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:         42,
		Title:       "Fix critical bug",
		Description: "Database connection timeout",
		State:       "opened",
		Labels:      []string{"bug", "critical"},
		Author:      "alice",
		WebURL:      "https://gitlab.com/project/issues/42",
		Created:     baseTime,
		Updated:     baseTime.Add(time.Hour),
	}

	doc := DocumentFromIssue("my-project", issue)

	if doc.Source != index.SourceGitLabIssue {
		t.Errorf("expected Source %q, got %q", index.SourceGitLabIssue, doc.Source)
	}
	if doc.SourceID != "my-project/issues/42" {
		t.Errorf("expected SourceID %q, got %q", "my-project/issues/42", doc.SourceID)
	}
	if doc.Title != "Fix critical bug" {
		t.Errorf("expected Title %q, got %q", "Fix critical bug", doc.Title)
	}
	if doc.BodyHash == "" {
		t.Errorf("expected BodyHash to be set")
	}

	if project, ok := doc.Metadata["project"].(string); !ok || project != "my-project" {
		t.Errorf("expected project %q in metadata", "my-project")
	}
	if iid, ok := doc.Metadata["iid"].(int); !ok || iid != 42 {
		t.Errorf("expected iid 42 in metadata")
	}
	if _, ok := doc.Metadata["gitlab_issue"]; !ok {
		t.Errorf("expected gitlab_issue in metadata")
	}
}

func TestDocumentFromMergeRequest(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	mr := MergeRequest{
		IID:         15,
		Title:       "Add new feature",
		Description: "Implements feature X",
		State:       "opened",
		Labels:      []string{"feature"},
		Author:      "bob",
		WebURL:      "https://gitlab.com/project/merge_requests/15",
		Created:     baseTime,
		Updated:     baseTime.Add(2 * time.Hour),
	}

	doc := DocumentFromMergeRequest("my-project", mr)

	if doc.Source != index.SourceGitLabMR {
		t.Errorf("expected Source %q, got %q", index.SourceGitLabMR, doc.Source)
	}
	if doc.SourceID != "my-project/merge_requests/15" {
		t.Errorf("expected SourceID %q, got %q", "my-project/merge_requests/15", doc.SourceID)
	}
	if _, ok := doc.Metadata["gitlab_mr"]; !ok {
		t.Errorf("expected gitlab_mr in metadata")
	}
}

func TestDocumentFromRelease(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	release := Release{
		TagName:     "v1.0.0",
		Name:        "Version 1.0.0",
		Description: "Initial release",
		ReleasedAt:  baseTime,
		WebURL:      "https://gitlab.com/project/releases/v1.0.0",
	}

	doc := DocumentFromRelease("my-project", release)

	if doc.Source != index.SourceGitLabRelease {
		t.Errorf("expected Source %q, got %q", index.SourceGitLabRelease, doc.Source)
	}
	if doc.SourceID != "my-project/releases/v1.0.0" {
		t.Errorf("expected SourceID %q, got %q", "my-project/releases/v1.0.0", doc.SourceID)
	}
	if auth, ok := doc.Metadata["authority"]; ok {
		if auth != string(index.AuthorityMediumHigh) {
			t.Errorf("expected authority %q, got %q", string(index.AuthorityMediumHigh), auth)
		}
	}
}

func TestChunkIssue(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:         42,
		Title:       "Test issue",
		Description: "Issue description",
		State:       "opened",
		Labels:      []string{"test"},
		Author:      "alice",
		WebURL:      "https://example.com",
		Created:     baseTime,
		Updated:     baseTime,
		Threads: []Thread{
			{
				ID:       "thread-1",
				Resolved: false,
				Comments: []Comment{
					{
						Author:  "bob",
						Body:    "First comment",
						Created: baseTime.Add(time.Hour),
					},
				},
			},
			{
				ID:       "thread-2",
				Resolved: false,
				Comments: []Comment{
					{
						Author:  "charlie",
						Body:    "ok",
						Created: baseTime.Add(2 * time.Hour),
					},
				},
			},
		},
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect: summary, description, comments (trivial "ok" filtered out)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	if chunks[0].Type != index.ChunkGitLabIssueSummary {
		t.Errorf("expected first chunk to be summary, got %q", chunks[0].Type)
	}
	if chunks[1].Type != index.ChunkGitLabIssueSummary {
		t.Errorf("expected second chunk to be summary (description), got %q", chunks[1].Type)
	}
	if chunks[2].Type != index.ChunkGitLabIssueComments {
		t.Errorf("expected third chunk to be comments, got %q", chunks[2].Type)
	}

	// Verify trivial comment is filtered out
	if strings.Contains(chunks[2].Text, "ok") {
		t.Errorf("trivial comment should be filtered out")
	}
	if !strings.Contains(chunks[2].Text, "First comment") {
		t.Errorf("expected substantial comment to be present")
	}
}

func TestChunkMergeRequest(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	mr := MergeRequest{
		IID:         15,
		Title:       "Test MR",
		Description: "MR description",
		State:       "opened",
		Labels:      []string{"feature"},
		Author:      "bob",
		WebURL:      "https://example.com",
		Created:     baseTime,
		Updated:     baseTime,
		Threads: []Thread{
			{
				ID:       "thread-1",
				Resolved: false,
				Comments: []Comment{
					{
						Author:  "alice",
						Body:    "Looks good!",
						Created: baseTime.Add(time.Hour),
					},
				},
			},
		},
	}

	doc := DocumentFromMergeRequest("project", mr)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect: summary, description, comments
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	if chunks[0].Type != index.ChunkGitLabMRSummary {
		t.Errorf("expected first chunk to be MR summary, got %q", chunks[0].Type)
	}
	if chunks[2].Type != index.ChunkGitLabMRComments {
		t.Errorf("expected third chunk to be MR comments, got %q", chunks[2].Type)
	}
}

func TestChunkRelease(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	release := Release{
		TagName:     "v1.0.0",
		Name:        "Version 1.0.0",
		Description: "Release notes\n\n- Feature 1\n- Feature 2",
		ReleasedAt:  baseTime,
		WebURL:      "https://example.com",
	}

	doc := DocumentFromRelease("project", release)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	if chunks[0].Type != index.ChunkGitLabReleaseNotes {
		t.Errorf("expected chunk type %q, got %q", index.ChunkGitLabReleaseNotes, chunks[0].Type)
	}
	if !strings.Contains(chunks[0].Text, "Feature 1") {
		t.Errorf("expected release notes content in chunk")
	}
}

func TestChunkMetadataExclusion(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:     1,
		Title:   "Test",
		State:   "opened",
		Created: baseTime,
		Updated: baseTime,
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, chunk := range chunks {
		if _, ok := chunk.Metadata["gitlab_issue"]; ok {
			t.Errorf("chunk metadata should not contain gitlab_issue")
		}
		if project, ok := chunk.Metadata["project"].(string); !ok || project != "project" {
			t.Errorf("expected project in chunk metadata")
		}
	}
}

func TestChunkIndexing(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:         1,
		Title:       "Test",
		Description: "Description",
		State:       "opened",
		Created:     baseTime,
		Updated:     baseTime,
		Threads: []Thread{
			{
				ID:       "thread-1",
				Resolved: false,
				Comments: []Comment{
					{Author: "alice", Body: "Comment 1", Created: baseTime.Add(time.Hour)},
				},
			},
			{
				ID:       "thread-2",
				Resolved: false,
				Comments: []Comment{
					{Author: "bob", Body: "Comment 2", Created: baseTime.Add(2 * time.Hour)},
				},
			},
		},
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify sequential indices
	for i, chunk := range chunks {
		if chunk.Index != i {
			t.Errorf("chunk %d: expected Index %d, got %d", i, i, chunk.Index)
		}
		if chunk.DocumentID != doc.ID {
			t.Errorf("chunk %d: expected DocumentID to match", i)
		}
	}
}

func TestCommentGrouping(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	// Create 10 substantive threads to trigger multiple groups (8 per group)
	var threads []Thread
	for i := range 10 {
		threads = append(threads, Thread{
			ID:       "thread-" + string(rune('0'+i)),
			Resolved: false,
			Comments: []Comment{
				{
					Author:  "user" + string(rune('0'+i)),
					Body:    "Comment " + string(rune('0'+i)),
					Created: baseTime.Add(time.Duration(i) * time.Hour),
				},
			},
		})
	}

	issue := Issue{
		IID:     1,
		Title:   "Test",
		State:   "opened",
		Created: baseTime,
		Updated: baseTime,
		Threads: threads,
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect: summary + 2 comment groups
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (summary + 2 groups), got %d", len(chunks))
	}

	if chunks[1].Type != index.ChunkGitLabIssueComments || chunks[2].Type != index.ChunkGitLabIssueComments {
		t.Errorf("expected chunks 1 and 2 to be comment groups")
	}
}

func TestAllTrivialCommentsFiltered(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:     1,
		Title:   "Test",
		State:   "opened",
		Created: baseTime,
		Updated: baseTime,
		Threads: []Thread{
			{
				ID:       "thread-1",
				Resolved: false,
				Comments: []Comment{
					{Author: "a", Body: "ok", Created: baseTime.Add(time.Hour)},
				},
			},
			{
				ID:       "thread-2",
				Resolved: false,
				Comments: []Comment{
					{Author: "b", Body: "done", Created: baseTime.Add(2 * time.Hour)},
				},
			},
			{
				ID:       "thread-3",
				Resolved: false,
				Comments: []Comment{
					{Author: "c", Body: "+1", Created: baseTime.Add(3 * time.Hour)},
				},
			},
		},
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have summary, no comment chunks
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (summary only), got %d", len(chunks))
	}
	if chunks[0].Type != index.ChunkGitLabIssueSummary {
		t.Errorf("expected summary chunk")
	}
}

func TestEmptyRelease(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	release := Release{
		TagName:     "v1.0.0",
		Name:        "",
		Description: "",
		ReleasedAt:  baseTime,
		WebURL:      "https://example.com",
	}

	doc := DocumentFromRelease("project", release)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return nil for empty release (no description and name)
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for empty release, got %d", len(chunks))
	}
}

func TestChunkTextHash(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:         1,
		Title:       "Test",
		Description: "Test description",
		State:       "opened",
		Created:     baseTime,
		Updated:     baseTime,
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, chunk := range chunks {
		if chunk.TextHash == "" {
			t.Errorf("chunk %d: expected non-empty TextHash", i)
		}
		expectedHash := index.Hash(chunk.Text)
		if chunk.TextHash != expectedHash {
			t.Errorf("chunk %d: hash mismatch", i)
		}
	}
}

func TestResolvedThread(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:     1,
		Title:   "Test issue with resolved thread",
		State:   "opened",
		Created: baseTime,
		Updated: baseTime,
		Threads: []Thread{
			{
				ID:       "thread-1",
				Resolved: true,
				Comments: []Comment{
					{
						Author:  "alice",
						Body:    "This is resolved",
						Created: baseTime.Add(time.Hour),
					},
				},
			},
			{
				ID:       "thread-2",
				Resolved: false,
				Comments: []Comment{
					{
						Author:  "bob",
						Body:    "This is not resolved",
						Created: baseTime.Add(2 * time.Hour),
					},
				},
			},
		},
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the comment chunk
	var commentChunk *index.Chunk
	for i := range chunks {
		if chunks[i].Type == index.ChunkGitLabIssueComments {
			commentChunk = &chunks[i]
			break
		}
	}

	if commentChunk == nil {
		t.Fatalf("expected comment chunk to be present")
	}

	// Check that resolved marker is present
	if !strings.Contains(commentChunk.Text, "[resolved]") {
		t.Errorf("expected [resolved] marker in comment chunk")
	}

	// Check that both threads are present
	if !strings.Contains(commentChunk.Text, "This is resolved") {
		t.Errorf("expected resolved comment in chunk")
	}
	if !strings.Contains(commentChunk.Text, "This is not resolved") {
		t.Errorf("expected unresolved comment in chunk")
	}
}

func TestTrivialThreadDropped(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	issue := Issue{
		IID:     1,
		Title:   "Test issue with trivial thread",
		State:   "opened",
		Created: baseTime,
		Updated: baseTime,
		Threads: []Thread{
			{
				ID:       "thread-1",
				Resolved: false,
				Comments: []Comment{
					{
						Author:  "alice",
						Body:    "ok",
						Created: baseTime.Add(time.Hour),
					},
				},
			},
			{
				ID:       "thread-2",
				Resolved: false,
				Comments: []Comment{
					{
						Author:  "bob",
						Body:    "Substantial comment",
						Created: baseTime.Add(2 * time.Hour),
					},
				},
			},
		},
	}

	doc := DocumentFromIssue("project", issue)
	chunker := New()

	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the comment chunk
	var commentChunk *index.Chunk
	for i := range chunks {
		if chunks[i].Type == index.ChunkGitLabIssueComments {
			commentChunk = &chunks[i]
			break
		}
	}

	if commentChunk == nil {
		t.Fatalf("expected comment chunk to be present")
	}

	// Check that trivial comment is not present
	if strings.Contains(commentChunk.Text, "ok") {
		t.Errorf("expected trivial comment to be filtered out")
	}

	// Check that substantial comment is present
	if !strings.Contains(commentChunk.Text, "Substantial comment") {
		t.Errorf("expected substantial comment in chunk")
	}
}
