package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var ruleNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func testRule() CommentRule {
	return CommentRule{
		Source:     SourceGitLab,
		Commented:  EventMRCommented,
		Mentioned:  EventMRMentioned,
		ButtonText: "Open comment",
		Staleness:  Staleness{Now: func() time.Time { return ruleNow }},
	}
}

var testSubject = CommentSubject{
	ObjectID: "group/proj!1",
	Title:    "MR !1: Fix bug",
	URL:      "https://example.com/mr/1",
}

func watcher(key string) Actor { return Actor{Source: SourceGitLab, Key: key} }

// newComment builds a fixture comment authored ago before the fixture clock.
func newComment(id, author string, ago time.Duration, body string, mentions ...string) Comment {
	return Comment{
		ID:        id,
		Author:    Actor{Source: SourceGitLab, Key: author},
		Body:      body,
		Mentions:  mentions,
		URL:       "https://example.com/mr/1#note_" + id,
		CreatedAt: ruleNow.Add(-ago),
	}
}

// byType indexes projected events by (type, recipient).
func byType(events []Event) map[string]Event {
	out := make(map[string]Event, len(events))
	for _, e := range events {
		out[string(e.Type)+"/"+e.Recipient.Key] = e
	}
	return out
}

func TestCommentRule_NotifiesWatchers(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice"), watcher("bob")},
		[]Comment{newComment("10", "carol", time.Minute, "please take a look")},
	)

	require.Len(t, events, 2)
	got := byType(events)

	alice := got["mr_commented/alice"]
	require.Equal(t, SourceGitLab, alice.Source)
	require.Equal(t, "carol", alice.Actor.Key)
	require.Equal(t, "MR !1: Fix bug", alice.Title)
	require.Equal(t, "please take a look", alice.Description)
	require.Equal(t, "https://example.com/mr/1#note_10", alice.URL)
	require.Equal(t, []Button{{Text: "Open comment", URL: "https://example.com/mr/1#note_10"}}, alice.Buttons)
	require.Equal(t, "group/proj!1", alice.ObjectID)
	require.Equal(t, "mr_commented:group/proj!1:10:alice", alice.EventID)
	// The comment's own time, not the poll's: it is when the thing happened.
	require.Equal(t, ruleNow.Add(-time.Minute), alice.OccurredAt)

	require.Contains(t, got, "mr_commented/bob")
}

// A comment you wrote must not notify you — and, crucially, must not silence
// the one behind it that someone else wrote.
func TestCommentRule_OwnCommentDoesNotShadowOlderOne(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice")},
		[]Comment{
			newComment("10", "carol", 2*time.Minute, "please take a look"),
			newComment("11", "alice", time.Minute, "on it"),
		},
	)

	require.Len(t, events, 1)
	require.Equal(t, "mr_commented:group/proj!1:10:alice", events[0].EventID)
	require.Equal(t, "carol", events[0].Actor.Key)
}

// A burst between two polls is one message, keyed by the newest comment, so
// the next poll's newest comment is still a fresh notification.
func TestCommentRule_CoalescesBurstToNewest(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice")},
		[]Comment{
			newComment("10", "carol", 3*time.Minute, "one"),
			newComment("11", "carol", 2*time.Minute, "two"),
			newComment("12", "dave", time.Minute, "three"),
		},
	)

	require.Len(t, events, 1)
	require.Equal(t, "mr_commented:group/proj!1:12:alice", events[0].EventID)
	require.Equal(t, "three", events[0].Description)
}

func TestCommentRule_MentionNotifiesPerComment(t *testing.T) {
	events := testRule().Project(testSubject, nil,
		[]Comment{
			newComment("10", "carol", 2*time.Minute, "@erin thoughts?", "erin"),
			newComment("11", "carol", time.Minute, "@erin ping", "erin"),
		},
	)

	require.Len(t, events, 2)
	for _, e := range events {
		require.Equal(t, EventMRMentioned, e.Type)
		require.Equal(t, "erin", e.Recipient.Key)
		require.Equal(t, SourceGitLab, e.Recipient.Source)
	}
	require.Equal(t, "mr_mentioned:group/proj!1:10:erin", events[0].EventID)
	require.Equal(t, "mr_mentioned:group/proj!1:11:erin", events[1].EventID)
}

// A mention reaches someone who has nothing else to do with the object.
func TestCommentRule_MentionNeedsNoWatchership(t *testing.T) {
	events := testRule().Project(testSubject, []Actor{watcher("alice")},
		[]Comment{newComment("10", "carol", time.Minute, "@erin thoughts?", "erin")},
	)

	got := byType(events)
	require.Contains(t, got, "mr_mentioned/erin")
	require.Contains(t, got, "mr_commented/alice")
	require.Len(t, events, 2)
}

// Mentioned in the newest comment: one message, not two.
func TestCommentRule_MentionSupersedesComment(t *testing.T) {
	events := testRule().Project(testSubject, []Actor{watcher("alice")},
		[]Comment{
			newComment("10", "carol", 2*time.Minute, "context"),
			newComment("11", "carol", time.Minute, "@alice thoughts?", "alice"),
		},
	)

	require.Len(t, events, 1)
	require.Equal(t, EventMRMentioned, events[0].Type)
	require.Equal(t, "mr_mentioned:group/proj!1:11:alice", events[0].EventID)
}

// The backfill guard: without it the first poll after this shipped would
// announce every comment in the fetched window.
func TestCommentRule_DropsStaleComments(t *testing.T) {
	events := testRule().Project(testSubject, []Actor{watcher("alice")},
		[]Comment{
			newComment("10", "carol", 48*time.Hour, "last month", "erin"),
			newComment("11", "carol", time.Minute, "today"),
		},
	)

	require.Len(t, events, 1)
	require.Equal(t, "mr_commented:group/proj!1:11:alice", events[0].EventID)
}

// A comment with no id cannot be deduplicated, so it must not notify at all.
func TestCommentRule_SkipsCommentWithoutID(t *testing.T) {
	events := testRule().Project(testSubject, []Actor{watcher("alice")},
		[]Comment{newComment("", "carol", time.Minute, "anonymous", "erin")},
	)
	require.Empty(t, events)
}

func TestCommentRule_FallsBackToObjectURL(t *testing.T) {
	c := newComment("10", "carol", time.Minute, "hi")
	c.URL = ""
	events := testRule().Project(testSubject, []Actor{watcher("alice")}, []Comment{c})

	require.Len(t, events, 1)
	require.Equal(t, testSubject.URL, events[0].URL)
	require.Equal(t, testSubject.URL, events[0].Buttons[0].URL)
}

// Both an assignee and a reviewer is still one person.
func TestCommentRule_DedupesWatchers(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice"), watcher("alice"), {Source: SourceGitLab, Key: ""}},
		[]Comment{newComment("10", "carol", time.Minute, "hi")},
	)
	require.Len(t, events, 1)
}

func TestSnippet(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello there", "hello there"},
		{"collapses whitespace", "hello    there\t\tfriend", "hello there friend"},
		{"drops blank lines", "one\n\n\ntwo", "one" + LineBreak + "two"},
		{"normalizes crlf", "one\r\ntwo", "one" + LineBreak + "two"},
		{"empty", "   \n\t ", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Snippet(tt.in))
		})
	}
}

func TestSnippet_TruncatesAtWordBoundary(t *testing.T) {
	got := Snippet(strings.Repeat("word ", 200))
	require.True(t, strings.HasSuffix(got, "…"), got)
	require.LessOrEqual(t, len([]rune(got)), MaxSnippetRunes+1)
	require.False(t, strings.HasSuffix(strings.TrimSuffix(got, "…"), "wor"), "cut mid-word: %q", got)
}

// A cut must not land inside a multi-byte character.
func TestSnippet_TruncatesOnRunes(t *testing.T) {
	got := Snippet(strings.Repeat("м", MaxSnippetRunes+50))
	require.True(t, strings.HasSuffix(got, "…"))
	require.Equal(t, MaxSnippetRunes+1, len([]rune(got)))
}

// inThread returns c reassigned to a discussion thread.
func inThread(c Comment, thread string) Comment {
	c.ThreadID = thread
	return c
}

// Two review remarks left on two lines of a diff, inside one poll window, are
// two pieces of news. Coalescing them onto the newest is what silently dropped
// the middle one of three comments on a real merge request.
func TestCommentRule_CoalescesPerThread(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice")},
		[]Comment{
			inThread(newComment("10", "carol", 3*time.Minute, "should it be vmauth instead?"), "d1"),
			inThread(newComment("11", "carol", 2*time.Minute, "-remoteWrite.maxDiskUsagePerURL=1GB"), "d2"),
			inThread(newComment("12", "carol", time.Minute, "sizeLimit: 2Gi"), "d3"),
		},
	)

	require.Len(t, events, 3)
	var ids []string
	for _, e := range events {
		require.Equal(t, EventMRCommented, e.Type)
		require.Equal(t, "alice", e.Recipient.Key)
		ids = append(ids, e.EventID)
	}
	require.Equal(t, []string{
		"mr_commented:group/proj!1:10:alice",
		"mr_commented:group/proj!1:11:alice",
		"mr_commented:group/proj!1:12:alice",
	}, ids)
}

// Replies piling up inside one thread still collapse to its newest: that is
// the case the coalescing exists for.
func TestCommentRule_CollapsesRepliesWithinThread(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice")},
		[]Comment{
			inThread(newComment("10", "carol", 3*time.Minute, "one"), "d1"),
			inThread(newComment("11", "dave", 2*time.Minute, "two"), "d1"),
			inThread(newComment("12", "carol", time.Minute, "three"), "d1"),
		},
	)

	require.Len(t, events, 1)
	require.Equal(t, "mr_commented:group/proj!1:12:alice", events[0].EventID)
	require.Equal(t, "three", events[0].Description)
}

// A source with no threads (Jira) leaves ThreadID empty, which must keep the
// one-message-per-object behavior rather than fanning out per comment.
func TestCommentRule_ThreadlessSourceStaysCoalesced(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice")},
		[]Comment{
			newComment("10", "carol", 2*time.Minute, "one"),
			newComment("11", "carol", time.Minute, "two"),
		},
	)

	require.Len(t, events, 1)
	require.Equal(t, "mr_commented:group/proj!1:11:alice", events[0].EventID)
}

// A mention in one thread must not silence the comment event for another.
func TestCommentRule_MentionSuppressesOnlyItsOwnThread(t *testing.T) {
	events := testRule().Project(testSubject,
		[]Actor{watcher("alice")},
		[]Comment{
			inThread(newComment("10", "carol", 2*time.Minute, "@alice thoughts?", "alice"), "d1"),
			inThread(newComment("11", "carol", time.Minute, "unrelated remark"), "d2"),
		},
	)

	got := byType(events)
	require.Len(t, events, 2)
	require.Contains(t, got, "mr_mentioned/alice")
	require.Equal(t, "mr_commented:group/proj!1:11:alice", got["mr_commented/alice"].EventID)
}

func TestCommentRule_IgnoredAuthorNotifiesNobody(t *testing.T) {
	rule := testRule()
	rule.Ignored = NewIgnoredAuthors([]ActorKey{{Source: SourceGitLab, Key: "automation"}})

	out := rule.Project(testSubject, []Actor{watcher("alice")}, []Comment{
		newComment("1", "automation", time.Minute, "linked ABC-1", "alice"),
	})
	require.Empty(t, out)
}

// The bot's comment must not be picked as the thread's newest and coalesce the
// human reply behind it out of existence.
func TestCommentRule_IgnoredAuthorDoesNotShadowHuman(t *testing.T) {
	rule := testRule()
	rule.Ignored = NewIgnoredAuthors([]ActorKey{{Source: SourceGitLab, Key: "automation"}})

	out := rule.Project(testSubject, []Actor{watcher("alice")}, []Comment{
		newComment("1", "bob", 2*time.Minute, "does this look right?"),
		newComment("2", "automation", time.Minute, "linked ABC-1"),
	})
	require.Len(t, out, 1)
	require.Equal(t, "bob", out[0].Actor.Key)
	require.Equal(t, "does this look right?", out[0].Description)
}

// The set is per source: the same key in the other id space is a different
// account.
func TestIgnoredAuthors_ScopedBySource(t *testing.T) {
	set := NewIgnoredAuthors([]ActorKey{{Source: SourceJira, Key: "automation"}})
	require.True(t, set.Has(Actor{Source: SourceJira, Key: "automation"}))
	require.False(t, set.Has(Actor{Source: SourceGitLab, Key: "automation"}))
	require.False(t, set.Has(Actor{Source: SourceJira}))
	require.False(t, NewIgnoredAuthors(nil).Has(Actor{Source: SourceJira, Key: "automation"}))
}
