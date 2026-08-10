package jira

import (
	"context"
	"strings"
	"time"

	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
)

// assigneeField is the changelog item field name for an assignment change.
// Jira Cloud also sends a machine-readable fieldId; Server/DC sends only
// field, whose value is localized in some deployments — matching both is the
// best either API offers.
const assigneeField = "assignee"

// jiraChangelog is an issue's history, as returned under expand=changelog.
//
// StartAt/MaxResults/Total are paging metadata Jira sends alongside the
// entries, and they are read for one reason: they are the only way to tell
// "this issue has no assignment history" from "this response does not contain
// it". [complete] turns them into that answer, and [assignedAtCreation]
// depends on it.
type jiraChangelog struct {
	StartAt    int           `json:"startAt"`
	MaxResults int           `json:"maxResults"`
	Total      int           `json:"total"`
	Histories  []jiraHistory `json:"histories"`
}

// complete reports whether these are all of the issue's history entries, not a
// page of them.
//
// A nil changelog is not complete: it means the caller did not ask for one (or
// Jira declined), which proves nothing about the issue's history. Deployments
// that omit the paging fields entirely report Total 0, and any entry count
// clears that — a deliberate lean towards trusting the response, since the
// alternative is disabling [assignedAtCreation] wherever the fields are
// missing.
func (cl *jiraChangelog) complete() bool {
	if cl == nil {
		return false
	}
	return cl.StartAt == 0 && len(cl.Histories) >= cl.Total
}

// assignedAtCreation reports whether the issue's current assignee has held the
// assignment since the issue was filed.
//
// Jira writes a changelog entry for a field it sees *change*, and a field set
// on the create screen never changed — so an issue created already assigned
// carries no assignment history at all, and [changelogActors] can only report
// an unknown AssignedAt for it. notify.Staleness reads unknown as "notify
// anyway", so such an assignment is announced as news by whatever unrelated
// edit first brings the issue past an incremental poll — a sprint rollover
// touching a ticket assigned weeks ago is enough.
//
// The absence of an entry is only evidence when the whole history is in hand,
// hence [complete]. A truncated changelog leaves AssignedAt unknown, which is
// the pre-existing, permissive behavior.
func assignedAtCreation(cl *jiraChangelog) bool {
	if !cl.complete() {
		return false
	}
	for _, h := range cl.Histories {
		if h.touchesAssignee() {
			return false
		}
	}
	return true
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

// IssueActors are the people an issue's changelog names — who touched it last
// and who set the current assignee — plus when that assignment happened, which
// is what tells a destination whether it is news or history.
type IssueActors struct {
	UpdatedBy  chunkjira.User
	AssignedBy chunkjira.User
	AssignedAt time.Time
}

// lastEntry returns the author and timestamp of the newest history entry
// matching want, or zero values when no entry matches.
//
// Entries whose timestamp does not parse are skipped rather than failing the
// issue: the changelog is auxiliary to every other field, and losing an actor
// is a rendering downgrade ("Someone" instead of a name) where losing the
// issue is a missing notification.
//
// An entry naming no author still counts, and yields a zero user with a real
// timestamp. Who and when are separate answers: an assignment made by a
// workflow post-function or a since-deleted account has no author Jira can
// report, but it happened at a knowable moment, and that moment is what
// [notify.Staleness] gates on. Dropping the entry outright surrendered both,
// so such an assignment reached the projector as "unknown time", which
// staleness lets through however old it is.
//
// The author is taken from the newest matching entry only, never from an
// older one that happens to name somebody: the entry is the change, and
// borrowing a name from a previous change attributes it to the wrong person.
//
// The newest entry is found by comparing timestamps, not by taking the last
// element: Jira orders histories oldest-first, but nothing in the API
// guarantees it and a wrong pick here misattributes an action to a colleague.
func lastEntry(ctx context.Context, cl *jiraChangelog, baseURL, key string, want func(jiraHistory) bool) (chunkjira.User, time.Time) {
	if cl == nil {
		return chunkjira.User{}, time.Time{}
	}

	var (
		best      jiraHistory
		bestAt    time.Time
		bestFound bool
	)
	for _, h := range cl.Histories {
		if !want(h) {
			continue
		}
		created, err := parseJiraTime(h.Created)
		if err != nil || created.IsZero() {
			zctx.From(ctx).Warn("jira: changelog entry has no usable timestamp, ignoring it",
				zap.String("key", key),
				zap.String("created", h.Created),
				zap.Error(err),
			)
			continue
		}
		if bestFound && !created.After(bestAt) {
			continue
		}
		best, bestAt, bestFound = h, created, true
	}
	if !bestFound {
		return chunkjira.User{}, time.Time{}
	}
	if best.Author == nil {
		zctx.From(ctx).Warn("jira: changelog entry names no author, keeping its timestamp",
			zap.String("key", key),
			zap.Time("changed_at", bestAt),
		)
		return chunkjira.User{}, bestAt
	}
	return chunkjira.User{
		ID:      best.Author.identity(),
		Display: best.Author.DisplayName,
		URL:     best.Author.profileURL(baseURL),
	}, bestAt
}

// changelogActors extracts what an issue's changelog says about who acted. key
// is the issue key, for the logs an unusable entry emits.
func changelogActors(ctx context.Context, cl *jiraChangelog, baseURL, key string) IssueActors {
	var actors IssueActors
	actors.UpdatedBy, _ = lastEntry(ctx, cl, baseURL, key, func(jiraHistory) bool { return true })
	actors.AssignedBy, actors.AssignedAt = lastEntry(ctx, cl, baseURL, key, jiraHistory.touchesAssignee)
	return actors
}
