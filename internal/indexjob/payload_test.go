package indexjob

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/index"
)

func mustEncode(t *testing.T, kind Kind, doc index.Document) []byte {
	t.Helper()
	b, err := Encode(kind, doc)
	require.NoError(t, err)
	return b
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func gitlabIssue() chunkgitlab.Issue {
	return chunkgitlab.Issue{
		IID:         42,
		Title:       "Login times out",
		Description: "Users report a timeout on sign-in.",
		State:       "opened",
		Labels:      []string{"bug", "auth"},
		Author:      "alice",
		WebURL:      "https://gitlab.example.com/group/proj/-/issues/42",
		Created:     ts("2026-01-02T03:04:05Z"),
		Updated:     ts("2026-01-03T03:04:05Z"),
		Assignees:   []string{"bob"},
		Threads: []chunkgitlab.Thread{{
			ID:       "thread-1",
			Resolved: true,
			Comments: []chunkgitlab.Comment{
				{Author: "bob", Body: "Reproduced.", Created: ts("2026-01-02T09:00:00Z")},
				{Author: "alice", Body: "Fixed in !7.", Created: ts("2026-01-02T10:00:00Z")},
			},
		}},
		Links: []chunkgitlab.Link{{
			Type: "relates_to", TargetKind: "issue", TargetIID: 7,
			Title: "Session store flakiness", WebURL: "https://gitlab.example.com/group/proj/-/issues/7",
		}},
	}
}

func gitlabMR() chunkgitlab.MergeRequest {
	return chunkgitlab.MergeRequest{
		IID:            7,
		Title:          "Raise session timeout",
		Description:    "Bumps the timeout to 30s.",
		State:          "merged",
		Labels:         []string{"auth"},
		Author:         "alice",
		WebURL:         "https://gitlab.example.com/group/proj/-/merge_requests/7",
		Created:        ts("2026-01-02T03:04:05Z"),
		Updated:        ts("2026-01-04T03:04:05Z"),
		Assignees:      []string{"bob"},
		Reviewers:      []string{"carol"},
		Draft:          false,
		TargetBranch:   "main",
		SourceBranch:   "fix/session-timeout",
		MergedAt:       ts("2026-01-04T03:04:05Z"),
		MergedBy:       "carol",
		MergeCommitSHA: "0123456789abcdef0123456789abcdef01234567",
		Threads: []chunkgitlab.Thread{{
			ID:       "thread-9",
			Resolved: false,
			Comments: []chunkgitlab.Comment{{Author: "carol", Body: "LGTM", Created: ts("2026-01-03T12:00:00Z")}},
		}},
		Links: []chunkgitlab.Link{{
			Type: "closes", TargetKind: "issue", TargetIID: 42,
			Title: "Login times out", WebURL: "https://gitlab.example.com/group/proj/-/issues/42",
		}},
	}
}

func gitlabRelease() chunkgitlab.Release {
	return chunkgitlab.Release{
		TagName:     "v1.2.0",
		Name:        "1.2.0",
		Description: "Session handling fixes.",
		ReleasedAt:  ts("2026-01-05T03:04:05Z"),
		WebURL:      "https://gitlab.example.com/group/proj/-/releases/v1.2.0",
	}
}

func jiraIssue() chunkjira.Issue {
	return chunkjira.Issue{
		Key:               "ABC-1",
		Title:             "Login times out",
		Description:       "Users report a timeout on sign-in.",
		Status:            "In Progress",
		Resolution:        "",
		Components:        []string{"auth"},
		Labels:            []string{"bug"},
		Assignee:          "Bob Builder",
		AssigneeAccountID: "acct-123",
		Reporter:          "Alice Adams",
		Created:           ts("2026-01-02T03:04:05Z"),
		Updated:           ts("2026-01-03T03:04:05Z"),
		Comments: []chunkjira.Comment{
			{Author: "Bob Builder", Body: "Reproduced.", Created: ts("2026-01-02T09:00:00Z")},
		},
		WebURL: "https://jira.example.com/browse/ABC-1",
	}
}

// normalize strips the per-call random identifiers and compares in the form
// the chunks are actually stored in — JSON — so a metadata value's Go type
// (int vs float64, []string vs []any) does not register as a difference.
//
// Those type shifts are real but deliberate; [Canonicalize] settles both the
// inline and the queued path on the same one. What must not shift is the
// chunking itself: a chunker falling back to its untyped path produces
// different chunk types, texts and counts, which this still catches.
func normalize(t *testing.T, chunks []index.Chunk) string {
	t.Helper()
	out := make([]index.Chunk, len(chunks))
	copy(out, chunks)
	for i := range out {
		out[i].ID = uuid.Nil
	}
	b, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	return string(b)
}

// TestRoundTripChunks is the guard on [Decode]'s rehydration.
//
// The GitLab and Jira chunkers recover a concrete struct from the document's
// metadata by type assertion. A plain JSON round-trip leaves a map[string]any
// there instead, the assertion fails, and the chunker silently produces its
// fallback single-chunk output rather than typed summary/comment chunks — no
// error, no log, just a permanently worse index. Nothing else in the codebase
// would notice, so this test is the only thing standing between a new
// struct-valued metadata key and that regression.
func TestRoundTripChunks(t *testing.T) {
	for _, tt := range []struct {
		name string
		kind Kind
		doc  index.Document
	}{
		{
			name: "gitlab issue",
			kind: KindGitLab,
			doc:  chunkgitlab.DocumentFromIssue("group/proj", gitlabIssue()),
		},
		{
			name: "gitlab merge request",
			kind: KindGitLab,
			doc:  chunkgitlab.DocumentFromMergeRequest("group/proj", gitlabMR()),
		},
		{
			name: "gitlab release",
			kind: KindGitLab,
			doc:  chunkgitlab.DocumentFromRelease("group/proj", gitlabRelease()),
		},
		{
			name: "jira issue",
			kind: KindJira,
			doc:  chunkjira.DocumentFromIssue(jiraIssue()),
		},
		{
			name: "markdown",
			kind: KindMarkdown,
			doc: index.Document{
				ID:       uuid.MustParse("11111111-1111-1111-1111-111111111111"),
				Source:   index.Source("git_docs:group/proj"),
				SourceID: "docs/readme.md",
				URL:      "https://gitlab.example.com/group/proj/-/blob/main/docs/readme.md",
				Title:    "Readme",
				Body:     "# Title\n\nSome prose.\n\n## Section\n\nMore prose.\n",
				Metadata: map[string]any{"repo": "group/proj", "path": "docs/readme.md"},
			},
		},
		{
			name: "telegram",
			kind: KindTelegram,
			doc: index.Document{
				ID:       uuid.MustParse("22222222-2222-2222-2222-222222222222"),
				Source:   index.SourceTelegram,
				SourceID: "-100123:456",
				Title:    "Support thread",
				Body:     "user: it broke\nops: restarted it\n",
				Metadata: map[string]any{"summary": "restart fixed it", "chat_id": -100123},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			chunker, err := Chunker(tt.kind)
			require.NoError(t, err)

			want, err := chunker.Chunk(ctx, tt.doc)
			require.NoError(t, err)
			require.NotEmpty(t, want, "fixture must produce chunks or it proves nothing")

			raw, err := Encode(tt.kind, tt.doc)
			require.NoError(t, err)
			got, err := Decode(raw)
			require.NoError(t, err)
			require.Equal(t, tt.kind, got.Kind)

			gotChunks, err := chunker.Chunk(ctx, got.Document)
			require.NoError(t, err)

			require.Equal(t, normalize(t, want), normalize(t, gotChunks),
				"chunks differ after a queue round-trip: check Decode's rehydration")

			// Publishing what a worker decoded must not drift again.
			twice, err := Decode(mustEncode(t, got.Kind, got.Document))
			require.NoError(t, err)
			twiceChunks, err := chunker.Chunk(ctx, twice.Document)
			require.NoError(t, err)
			require.Equal(t, normalize(t, gotChunks), normalize(t, twiceChunks),
				"a second round-trip drifted: the canonical form is not a fixed point")
		})
	}
}

// TestRoundTripWithoutRehydrationDegrades pins the failure this rehydration
// exists to prevent, so the cost of dropping it is visible rather than
// theoretical: the chunker silently falls back instead of erroring.
func TestRoundTripWithoutRehydrationDegrades(t *testing.T) {
	ctx := context.Background()
	doc := chunkgitlab.DocumentFromIssue("group/proj", gitlabIssue())
	chunker := chunkgitlab.New()

	want, err := chunker.Chunk(ctx, doc)
	require.NoError(t, err)

	raw, err := Encode(KindGitLab, doc)
	require.NoError(t, err)

	// Decode without the rehydration step.
	var naive Payload
	require.NoError(t, json.Unmarshal(raw, &naive))
	got, err := chunker.Chunk(ctx, naive.Document)
	require.NoError(t, err, "the degradation is silent: chunking still succeeds")
	require.NotEqual(t, normalize(t, want), normalize(t, got))
}

func TestRehydrateIsIdempotent(t *testing.T) {
	doc := chunkjira.DocumentFromIssue(jiraIssue())
	raw, err := Encode(KindJira, doc)
	require.NoError(t, err)

	got, err := Decode(raw)
	require.NoError(t, err)
	require.NoError(t, rehydrate(got.Document.Metadata))

	iss, ok := got.Document.Metadata["jira_issue"].(chunkjira.Issue)
	require.True(t, ok)
	require.Equal(t, "ABC-1", iss.Key)
}

func TestDecodeRejectsGarbage(t *testing.T) {
	_, err := Decode([]byte("not json"))
	require.Error(t, err)
}

func TestChunkerUnknownKind(t *testing.T) {
	_, err := Chunker(Kind("nope"))
	require.Error(t, err)
}
