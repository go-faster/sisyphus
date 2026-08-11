package gitlab

import (
	"regexp"
	"slices"
	"strings"
	"time"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
)

// Comment is one comment on a merge request, as a destination that reacts to
// conversation needs it: who wrote it, what it says, who it names, and where
// to open it.
//
// It is a projection of [chunkgitlab.Comment], not that type re-used: the
// chunker's version carries the whole ingested body for indexing, while this
// one carries what a notification is built from — including Mentions, which
// only this package can extract because only it knows GitLab's "@username"
// syntax.
type Comment struct {
	// ID is the note id. It is stable across edits, so a notification keyed by
	// it fires once for a comment and not again when its text changes.
	ID string `json:"id"`
	// ThreadID is the discussion this comment belongs to. It is what a
	// destination coalesces on: two remarks on two lines of a diff are two
	// pieces of news, while twenty replies in one thread are one. Empty when
	// the source carried no discussion, which groups every comment together —
	// the behavior before threads were carried at all.
	ThreadID string           `json:"thread_id,omitempty"`
	Author   chunkgitlab.User `json:"author,omitzero"`
	Body     string           `json:"body"`
	// Mentions are the usernames the body names with "@". They are the
	// recipients of a mention notification, and unlike assignees they need not
	// have anything else to do with the merge request.
	Mentions []string `json:"mentions,omitempty"`
	// URL opens the comment itself: the merge request's own URL plus the note
	// anchor. It is derived from the subject's URL, never read out of content.
	URL       string    `json:"url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// maxPayloadComments caps how many comments an event carries. An event states
// the object's current state, so a merge request with a thousand comments
// would otherwise put all thousand on every poll. Only the newest few can be
// news; the rest were already seen (or are older than the projector's
// staleness cutoff anyway).
const maxPayloadComments = 20

// latestComments flattens the threads into the newest [maxPayloadComments]
// comments, oldest first. objectURL is the merge request's page, used to
// anchor each comment's own URL.
func latestComments(threads []chunkgitlab.Thread, objectURL string) []Comment {
	type threaded struct {
		thread  string
		comment chunkgitlab.Comment
	}
	var flat []threaded
	for _, t := range threads {
		for _, c := range t.Comments {
			flat = append(flat, threaded{thread: t.ID, comment: c})
		}
	}
	slices.SortStableFunc(flat, func(a, b threaded) int {
		return a.comment.Created.Compare(b.comment.Created)
	})
	if len(flat) > maxPayloadComments {
		flat = flat[len(flat)-maxPayloadComments:]
	}

	out := make([]Comment, 0, len(flat))
	for _, f := range flat {
		c := f.comment
		out = append(out, Comment{
			ID:        c.ID,
			ThreadID:  f.thread,
			Author:    c.AuthorUser,
			Body:      c.Body,
			Mentions:  Mentions(c.Body),
			URL:       commentURL(objectURL, c.ID),
			CreatedAt: c.Created,
		})
	}
	return out
}

// Resolution is one resolved discussion thread as an event payload carries it.
//
// It is self-contained rather than a pointer into [MRPayload.Comments]: those
// are capped at [maxPayloadComments], so on a busy merge request the thread's
// own comments may not be in the payload at all — and Participants is exactly
// who a resolution is addressed to.
type Resolution struct {
	ThreadID string           `json:"thread_id"`
	By       chunkgitlab.User `json:"by,omitzero"`
	// At is when the thread was resolved. Zero when GitLab's note did not
	// say, which a destination must read as "unknown", not as "old".
	At time.Time `json:"at,omitzero"`
	// Participants are everyone who commented in the thread, oldest comment
	// first and deduplicated. They are the people the conversation was
	// between, and so the people its ending is news to.
	Participants []chunkgitlab.User `json:"participants,omitempty"`
	// Excerpt is the thread's opening comment, so a notification can say which
	// thread was resolved rather than just that one was.
	Excerpt string `json:"excerpt,omitempty"`
	// URL opens the thread's first comment.
	URL string `json:"url,omitempty"`
}

// resolutions maps the fetched threads' resolved ones onto the payload shape.
// Like the member sets they are current state: a thread stays resolved in
// every payload after it is closed, so the destination gates on At.
func resolutions(threads []chunkgitlab.Thread, objectURL string) []Resolution {
	var out []Resolution
	for _, t := range threads {
		if !t.Resolved || t.ID == "" || len(t.Comments) == 0 {
			continue
		}
		var (
			participants []chunkgitlab.User
			seen         = make(map[string]struct{}, len(t.Comments))
		)
		for _, c := range t.Comments {
			if c.AuthorUser.Username == "" {
				continue
			}
			if _, ok := seen[c.AuthorUser.Username]; ok {
				continue
			}
			seen[c.AuthorUser.Username] = struct{}{}
			participants = append(participants, c.AuthorUser)
		}
		out = append(out, Resolution{
			ThreadID:     t.ID,
			By:           t.ResolvedBy,
			At:           t.ResolvedAt,
			Participants: participants,
			Excerpt:      t.Comments[0].Body,
			URL:          commentURL(objectURL, t.Comments[0].ID),
		})
	}
	return out
}

// commentURL points at one note on the object's own page. GitLab renders every
// note with an "note_<id>" anchor, so this is a fragment on a URL the API
// returned — not a URL guessed from the instance's base.
func commentURL(objectURL, noteID string) string {
	if objectURL == "" || noteID == "" {
		return ""
	}
	return objectURL + "#note_" + noteID
}

// mentionRe matches a GitLab "@username" mention. The leading group excludes
// the characters that would make the "@" part of something else — a word
// character or a "." before it means an email address, and a "/" means the
// tail of a path.
//
// Group mentions ("@group/subgroup") match only their first segment, which
// resolves to no configured identity and so notifies nobody: a group is not a
// person this gateway can address.
var mentionRe = regexp.MustCompile(`(^|[^\w@.\-/])@([a-zA-Z0-9][a-zA-Z0-9._\-]*)`)

// Mentions returns the usernames a comment body names with "@", in order of
// first appearance and without duplicates.
//
// It reads the raw body, quoted text and code spans included: telling those
// apart needs a Markdown parse, and the cost of the two errors is not
// symmetric — a mention inside a quote sends one message the dedup key
// collapses on the next poll, while missing a real one loses it silently.
func Mentions(body string) []string {
	var (
		out  []string
		seen = make(map[string]struct{})
	)
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		// A username ends alphanumeric, so trailing punctuation belongs to the
		// sentence, not the name.
		name := strings.TrimRight(m[2], "._-")
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
