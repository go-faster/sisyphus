package gitlab

import (
	"slices"
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
	// Approvals are the MR's standing approvals, one per approver, oldest
	// first. Unlike the fields above this is a list rather than a "last one
	// wins": two people approving are two separate pieces of news, and
	// collapsing them would silently drop one.
	Approvals []chunkgitlab.Approval
}

// System-note bodies GitLab writes for a membership change. They are
// English-only (GitLab does not localize system notes) and stable, but a
// wording change upstream degrades to an unknown actor, never to a wrong one.
var (
	assignPrefixes = []string{"assigned to ", "unassigned "}
	reviewPrefixes = []string{"requested review from ", "removed review request for "}
)

// Approval notes name no target — the note's own author is the approver — so
// they are matched whole rather than by prefix.
const (
	approveNote   = "approved this merge request"
	unapproveNote = "unapproved this merge request"
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

func isApprovalNote(body string) bool { return body == approveNote || body == unapproveNote }

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
	// Newest approve-or-unapprove note per approver. An approval is standing
	// state, so an approver who later unapproved must not still count as one —
	// only their newest note decides.
	latestApproval := make(map[string]approvalNote)
	for _, d := range discussions {
		for _, note := range d.Notes {
			if !note.System || note.Author == nil {
				continue
			}
			created, err := parseGitLabTime(note.CreatedAt)
			if err != nil || created.IsZero() {
				continue
			}
			user := convertGitLabUser(note.Author)
			if created.After(updatedAt) {
				actors.UpdatedBy, updatedAt = user, created
			}
			if isAssignmentNote(note.Body) && created.After(actors.AssignedAt) {
				actors.AssignedBy, actors.AssignedAt = user, created
			}
			if isReviewNote(note.Body) && created.After(actors.ReviewRequestedAt) {
				actors.ReviewRequestedBy, actors.ReviewRequestedAt = user, created
			}
			// Keyed on the username, which is also what a notification matches
			// a recipient on: an approver GitLab named only by a display name
			// cannot be addressed, and is dropped below.
			if isApprovalNote(note.Body) {
				if prev, ok := latestApproval[user.Username]; !ok || created.After(prev.at) {
					latestApproval[user.Username] = approvalNote{
						user:     user,
						at:       created,
						approved: note.Body == approveNote,
					}
				}
			}
		}
	}
	actors.Approvals = standingApprovals(latestApproval)
	return actors
}

// approvalNote is an approver's newest approve-or-unapprove note.
type approvalNote struct {
	user     chunkgitlab.User
	at       time.Time
	approved bool
}

// standingApprovals reduces the per-approver newest notes to the approvals
// still standing, oldest first. Ties break on username so the payload — and
// with it the event ids derived from it — is stable across polls.
func standingApprovals(latest map[string]approvalNote) []chunkgitlab.Approval {
	var out []chunkgitlab.Approval
	for username, n := range latest {
		if !n.approved || username == "" {
			continue
		}
		out = append(out, chunkgitlab.Approval{User: n.user, At: n.at})
	}
	slices.SortFunc(out, func(a, b chunkgitlab.Approval) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.User.Username, b.User.Username)
	})
	return out
}
