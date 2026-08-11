package notify

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Comment is one source-side comment reduced to what the shared projection
// rule below needs. A source adapter builds it: only that adapter knows how to
// read a comment id, an author identity, and a mention out of its own API.
type Comment struct {
	// ID is stable per comment and unchanged by an edit, so it is what the
	// dedup id is keyed on. A comment without one cannot be deduplicated and
	// is skipped.
	ID string
	// ThreadID is the discussion this comment belongs to, and the key comment
	// events coalesce on. Empty groups every comment on the object together,
	// which is what a source without threads (Jira) wants.
	ThreadID string
	Author   Actor
	Body     string
	// Mentions are the source-side keys the body names, in the same id space
	// as Recipient.Key.
	Mentions []string
	// URL opens this comment specifically; empty falls back to the object's.
	URL       string
	CreatedAt time.Time
}

// CommentSubject is the object the comments are on.
type CommentSubject struct {
	ObjectID string
	Title    string
	URL      string
}

// CommentRule projects an object's comments into per-recipient Events. GitLab
// and Jira differ only in their event types, their id space and the word on
// the button, so the rule itself — which is the part with the interesting
// failure modes — lives here once.
type CommentRule struct {
	Source    Source
	Commented EventType
	Mentioned EventType
	// ButtonText labels the button opening the comment ("Open comment").
	ButtonText string
	// Staleness is the age cutoff. A source event states the object's current
	// comments, not the new ones, so without it the first poll after this
	// feature ships announces every comment in the fetched window.
	Staleness Staleness
}

// Project fans comments out to watchers (the people the object is already
// addressed to: assignees, reviewers, the Jira assignee) and to whoever the
// comments name.
//
// Two different rules, because the two events have different volume:
//
//   - A mention notifies per comment. Being named is explicit and rare, and
//     silently collapsing two of them would lose a question addressed to you.
//   - A comment notifies once per (thread, recipient) per batch, for the
//     newest comment in that thread they did not write. Comments are unbounded
//     per object while assignment is one message ever, so a thread that gets
//     twenty replies between two polls must not become twenty messages. The
//     dedup id is keyed by that newest comment, so the next poll's newest
//     comment is a new notification and nothing is permanently suppressed.
//
// The coalescing key is the thread, not the object: two remarks on two lines
// of a diff are two pieces of news, and collapsing them to the newest loses a
// review comment outright — which is what happened when a reviewer left two
// twenty seconds apart, inside one poll window. Replies piling up inside one
// thread still collapse, because that is the case the coalescing exists for.
// A source with no threads leaves ThreadID empty on every comment, which
// groups them all and keeps the one-per-object behavior.
//
// A recipient the thread's newest comment already mentioned gets nothing
// further for it: they were told, and reaching further back in that thread
// would be noise. Comments a recipient wrote themselves never notify them,
// which [Event.SelfCaused] would also catch — but only after that comment had
// already been picked as the newest, silencing the real one behind it.
func (r CommentRule) Project(subject CommentSubject, watchers []Actor, comments []Comment) []Event {
	fresh := make([]Comment, 0, len(comments))
	for _, c := range comments {
		if c.ID == "" || !r.Staleness.Fresh(c.CreatedAt) {
			continue
		}
		fresh = append(fresh, c)
	}
	if len(fresh) == 0 {
		return nil
	}

	var out []Event
	// mentioned[commentID] is the set of keys that comment names, filled while
	// emitting mention events and reused to suppress their comment events.
	mentioned := make(map[string]map[string]struct{}, len(fresh))
	for _, c := range fresh {
		named := make(map[string]struct{}, len(c.Mentions))
		for _, key := range c.Mentions {
			if key == "" {
				continue
			}
			if _, ok := named[key]; ok {
				continue
			}
			named[key] = struct{}{}
			out = append(out, r.event(r.Mentioned, subject, c, Actor{Source: r.Source, Key: key}))
		}
		mentioned[c.ID] = named
	}

	// Grouped by thread, in order of each thread's first fresh comment, so the
	// projection is deterministic.
	var threads []string
	byThread := make(map[string][]Comment, len(fresh))
	for _, c := range fresh {
		if _, ok := byThread[c.ThreadID]; !ok {
			threads = append(threads, c.ThreadID)
		}
		byThread[c.ThreadID] = append(byThread[c.ThreadID], c)
	}

	seen := make(map[string]struct{}, len(watchers))
	for _, w := range watchers {
		if w.Key == "" {
			continue
		}
		if _, ok := seen[w.Key]; ok {
			continue
		}
		seen[w.Key] = struct{}{}

		for _, thread := range threads {
			// Newest first: the first comment in this thread the watcher did
			// not write is the one that notifies them.
			for _, c := range slices.Backward(byThread[thread]) {
				if c.Author.Key == w.Key {
					continue
				}
				if _, named := mentioned[c.ID][w.Key]; !named {
					out = append(out, r.event(r.Commented, subject, c, w))
				}
				break
			}
		}
	}
	return out
}

func (r CommentRule) event(t EventType, s CommentSubject, c Comment, recipient Actor) Event {
	url := c.URL
	if url == "" {
		url = s.URL
	}
	recipient.Source = r.Source
	return Event{
		Source:      r.Source,
		Type:        t,
		Recipient:   recipient,
		Actor:       c.Author,
		Title:       s.Title,
		Description: Snippet(c.Body),
		Buttons:     []Button{{Text: r.ButtonText, URL: url}},
		URL:         url,
		ObjectID:    s.ObjectID,
		// The comment id, not a timestamp: an edited comment keeps its id, so
		// editing does not re-notify.
		EventID:    fmt.Sprintf("%s:%s:%s:%s", t, s.ObjectID, c.ID, recipient.Key),
		OccurredAt: c.CreatedAt,
	}
}

// MaxSnippetRunes is how much of a comment a notification quotes. Long enough
// to tell whether the comment needs an answer, short enough that a chat full
// of them stays readable — the button is there for the rest.
const MaxSnippetRunes = 280

// Snippet reduces a comment body to the excerpt a notification quotes: blank
// lines dropped, each line's whitespace collapsed, and the whole thing cut to
// MaxSnippetRunes at a word boundary with an ellipsis.
//
// The result is still untrusted ingested content, and the renderer escapes it
// like any other plain-text field.
func Snippet(body string) string {
	var lines []string
	for line := range strings.SplitSeq(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	s := strings.Join(lines, LineBreak)

	// Counting runes, not bytes: a cut inside a multi-byte character produces
	// a broken glyph, and these bodies are not ASCII.
	if len([]rune(s)) <= MaxSnippetRunes {
		return s
	}
	cut := string([]rune(s)[:MaxSnippetRunes])
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \t\n") + "…"
}
