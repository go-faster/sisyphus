package jira

import (
	"regexp"
	"slices"
	"strings"
	"time"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
)

// Comment is one comment on an issue, as a destination that reacts to
// conversation needs it: who wrote it, what it says, who it names, and where
// to open it.
//
// It is a projection of [chunkjira.Comment], not that type re-used: the
// chunker's version carries the whole ingested body for indexing, while this
// one carries what a notification is built from — including Mentions, which
// only this package can extract because only it knows Jira's "[~user]" syntax.
type Comment struct {
	// ID is the comment id. It is stable across edits, so a notification keyed
	// by it fires once for a comment and not again when its text changes.
	ID     string         `json:"id"`
	Author chunkjira.User `json:"author,omitzero"`
	Body   string         `json:"body"`
	// Mentions are the identities the body names, in the same id space as
	// [IssuePayload.AssigneeAccountID]. They are the recipients of a mention
	// notification, and unlike the assignee they need not have anything else
	// to do with the issue.
	Mentions []string `json:"mentions,omitempty"`
	// URL opens the comment itself: the issue's own browse URL with Jira's
	// focused-comment parameter. It is derived from the subject's URL, never
	// read out of content.
	URL       string    `json:"url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// maxPayloadComments caps how many comments an event carries. An event states
// the issue's current state, so an issue with a thousand comments would
// otherwise put all thousand on every poll. Only the newest few can be news;
// the rest were already seen (or are older than the projector's staleness
// cutoff anyway).
const maxPayloadComments = 20

// latestComments keeps the newest [maxPayloadComments] comments, oldest first,
// with each body rendered out of wiki markup (see [plainText]) for the
// notification that quotes it.
func latestComments(iss chunkjira.Issue) []Comment {
	var (
		objectURL = iss.WebURL
		names     = mentionNames(iss)
	)

	flat := slices.Clone(iss.Comments)
	slices.SortStableFunc(flat, func(a, b chunkjira.Comment) int {
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
			Body:      plainText(c.Body, names),
			Mentions:  Mentions(c.Body),
			URL:       commentURL(objectURL, c.ID),
			CreatedAt: c.Created,
		})
	}
	return out
}

// mentionNames maps the ids a mention can name to the display names the issue
// already knows: its assignee and its commenters. Jira writes a mention as an
// id, and on Cloud that id is an opaque hash — so without a lookup the only
// readable rendering is an anonymous one. It resolves whoever is already part
// of the conversation, which is who a comment names in nearly every case.
func mentionNames(iss chunkjira.Issue) map[string]string {
	names := make(map[string]string, len(iss.Comments)+1)
	add := func(id, display string) {
		if id != "" && display != "" {
			names[id] = display
		}
	}
	add(iss.AssigneeAccountID, iss.Assignee)
	add(iss.UpdatedBy.ID, iss.UpdatedBy.Display)
	add(iss.AssignedBy.ID, iss.AssignedBy.Display)
	for _, c := range iss.Comments {
		add(c.AuthorUser.ID, c.AuthorUser.Display)
	}
	return names
}

// commentURL focuses one comment on the issue's own browse page. Both Cloud
// and Server/DC honor focusedCommentId, and a deployment that does not simply
// opens the issue — so this stays a parameter on a URL the API returned rather
// than a guess at a per-edition comment permalink.
func commentURL(objectURL, commentID string) string {
	if objectURL == "" || commentID == "" || strings.Contains(objectURL, "?") {
		return ""
	}
	return objectURL + "?focusedCommentId=" + commentID
}

// mentionRe matches a Jira mention: "[~accountid:557058:...]" on Cloud,
// "[~jsmith]" on Server/DC. The captured id is exactly what jiraUser.identity
// reports for the same person, which is what notify.identities is matched
// against.
var mentionRe = regexp.MustCompile(`\[~(?:accountid:)?([^\]\s]+)]`)

// Mentions returns the identities a comment body names, in order of first
// appearance and without duplicates.
//
// It reads the raw body, quoted text and code blocks included: telling those
// apart needs a wiki-markup parse, and the cost of the two errors is not
// symmetric — a mention inside a quote sends one message the dedup key
// collapses on the next poll, while missing a real one loses it silently.
func Mentions(body string) []string {
	var (
		out  []string
		seen = make(map[string]struct{})
	)
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		id := m[1]
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
