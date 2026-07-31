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
	ID     string           `json:"id"`
	Author chunkgitlab.User `json:"author,omitzero"`
	Body   string           `json:"body"`
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
	var flat []chunkgitlab.Comment
	for _, t := range threads {
		flat = append(flat, t.Comments...)
	}
	slices.SortStableFunc(flat, func(a, b chunkgitlab.Comment) int {
		return a.Created.Compare(b.Created)
	})
	if len(flat) > maxPayloadComments {
		flat = flat[len(flat)-maxPayloadComments:]
	}

	out := make([]Comment, 0, len(flat))
	for _, c := range flat {
		out = append(out, Comment{
			ID:        c.ID,
			Author:    c.AuthorUser,
			Body:      c.Body,
			Mentions:  Mentions(c.Body),
			URL:       commentURL(objectURL, c.ID),
			CreatedAt: c.Created,
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
