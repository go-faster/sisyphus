package jira

import (
	"strings"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
)

// assigneeField is the changelog item field name for an assignment change.
// Jira Cloud also sends a machine-readable fieldId; Server/DC sends only
// field, whose value is localized in some deployments — matching both is the
// best either API offers.
const assigneeField = "assignee"

type jiraChangelog struct {
	Histories []jiraHistory `json:"histories"`
}

type jiraHistory struct {
	Author  *jiraUser         `json:"author"`
	Created string            `json:"created"`
	Items   []jiraHistoryItem `json:"items"`
}

type jiraHistoryItem struct {
	Field   string `json:"field"`
	FieldID string `json:"fieldId"`
}

func (i jiraHistoryItem) isAssignee() bool {
	return strings.EqualFold(i.Field, assigneeField) || strings.EqualFold(i.FieldID, assigneeField)
}

func (h jiraHistory) touchesAssignee() bool {
	for _, item := range h.Items {
		if item.isAssignee() {
			return true
		}
	}
	return false
}

// lastAuthor returns the author of the newest history entry matching want, or
// the zero user when no entry matches or the matching ones name nobody.
//
// Entries whose timestamp does not parse are skipped rather than failing the
// issue: the changelog is auxiliary to every other field, and losing an actor
// is a rendering downgrade ("Someone" instead of a name) where losing the
// issue is a missing notification.
//
// The newest entry is found by comparing timestamps, not by taking the last
// element: Jira orders histories oldest-first, but nothing in the API
// guarantees it and a wrong pick here misattributes an action to a colleague.
func lastAuthor(cl *jiraChangelog, baseURL string, want func(jiraHistory) bool) chunkjira.User {
	if cl == nil {
		return chunkjira.User{}
	}

	var (
		best      jiraHistory
		bestFound bool
	)
	for _, h := range cl.Histories {
		if h.Author == nil || !want(h) {
			continue
		}
		created, err := parseJiraTime(h.Created)
		if err != nil || created.IsZero() {
			continue
		}
		if bestFound {
			bestCreated, err := parseJiraTime(best.Created)
			if err == nil && !created.After(bestCreated) {
				continue
			}
		}
		best, bestFound = h, true
	}
	if !bestFound {
		return chunkjira.User{}
	}
	return chunkjira.User{
		ID:      best.Author.identity(),
		Display: best.Author.DisplayName,
		URL:     best.Author.profileURL(baseURL),
	}
}

// changelogActors extracts the two actors an issue's changelog can name: who
// touched it last, and who last set its assignee.
func changelogActors(cl *jiraChangelog, baseURL string) (updatedBy, assignedBy chunkjira.User) {
	updatedBy = lastAuthor(cl, baseURL, func(jiraHistory) bool { return true })
	assignedBy = lastAuthor(cl, baseURL, jiraHistory.touchesAssignee)
	return updatedBy, assignedBy
}
