package gitlab

import (
	"strings"
	"time"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
)

// MRActors are the people an MR's system notes name: who touched it last, who
// last changed its assignees, and who last requested a review.
//
// GitLab exposes no assignment-events API, and the MR object itself carries
// only its author and its *current* members — never who put them there. The
// system notes it writes for every membership change are the only record, and
// the discussions endpoint already fetches them for comments, so reading them
// costs no extra request.
type MRActors struct {
	UpdatedBy         chunkgitlab.User
	AssignedBy        chunkgitlab.User
	ReviewRequestedBy chunkgitlab.User
	// AssignedAt and ReviewRequestedAt are when those changes happened, from
	// the same notes. Zero when unknown. A destination needs them to tell a
	// fresh membership change from one it is only seeing now because
	// something else on the MR changed.
	AssignedAt        time.Time
	ReviewRequestedAt time.Time
}

// System-note bodies GitLab writes for a membership change. They are
// English-only (GitLab does not localize system notes) and stable, but a
// wording change upstream degrades to an unknown actor, never to a wrong one.
var (
	assignPrefixes = []string{"assigned to ", "unassigned "}
	reviewPrefixes = []string{"requested review from ", "removed review request for "}
)

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func isAssignmentNote(body string) bool {
	// "unassigned @a and assigned @b" starts with an assign prefix already;
	// the Contains covers the reverse ordering GitLab uses when it reassigns.
	return hasAnyPrefix(body, assignPrefixes) || strings.Contains(body, " and assigned ")
}

func isReviewNote(body string) bool { return hasAnyPrefix(body, reviewPrefixes) }

// mrActors extracts the actors from an MR's discussions.
//
// The newest matching note wins, found by timestamp rather than by position:
// discussions are fetched sorted, but notes within one are not guaranteed to
// be, and a wrong pick here credits an action to the wrong colleague.
func mrActors(discussions []gitlabDiscussion) MRActors {
	var (
		actors    MRActors
		updatedAt time.Time
	)
	for _, d := range discussions {
		for _, note := range d.Notes {
			if !note.System || note.Author == nil {
				continue
			}
			created, err := parseGitLabTime(note.CreatedAt)
			if err != nil || created.IsZero() {
				continue
			}
			user := chunkgitlab.User{
				Username: note.Author.Username,
				Display:  note.Author.Name,
				URL:      note.Author.WebURL,
			}
			if created.After(updatedAt) {
				actors.UpdatedBy, updatedAt = user, created
			}
			if isAssignmentNote(note.Body) && created.After(actors.AssignedAt) {
				actors.AssignedBy, actors.AssignedAt = user, created
			}
			if isReviewNote(note.Body) && created.After(actors.ReviewRequestedAt) {
				actors.ReviewRequestedBy, actors.ReviewRequestedAt = user, created
			}
		}
	}
	return actors
}
