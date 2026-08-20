package gitlab

import (
	"fmt"
	"slices"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// projectConflict fans an event.TypeMRConflict event out into one
// EventMRConflict per recipient: the author first, then the assignees — the
// same set an MR's outcome is addressed to, and for the same reason. Only they
// can rebase it; telling reviewers that someone else's branch needs a rebase
// is noise.
//
// Staleness does not gate it. Every other standing-state event carries a
// timestamp saying when it became true (an approval, a merge, a resolution)
// and is dropped when that is old; a conflict carries none, because GitLab
// computes mergeability on demand and dates only the MR. What bounds the
// backlog instead is the sweep's own lookback window, which decides which open
// MRs are even looked at (see gitlab.conflicts.lookback_days).
//
// The dedup id is keyed on the head SHA, which is what makes a sweep that
// re-reports the same conflict every tick cost one notification: the id is
// unchanged until the author pushes, and a push that fails to resolve the
// conflict is a new head and news again.
func (pr Projector) projectConflict(e event.Event) ([]notify.Event, error) {
	var p ingestgitlab.ConflictPayload
	if err := e.DecodePayload(ingestgitlab.ConflictPayloadVersion, &p); err != nil {
		return nil, errors.Wrap(err, "decode conflict payload")
	}

	objectID := e.Subject.ID
	buttons := []notify.Button{{Text: "Open merge request", URL: e.Subject.URL}}
	description := conflictDescription(p)

	var out []notify.Event
	for _, username := range conflictRecipients(p) {
		out = append(out, notify.Event{
			Source:      notify.SourceGitLab,
			Type:        notify.EventMRConflict,
			Recipient:   notify.Actor{Source: notify.SourceGitLab, Key: username},
			Title:       e.Subject.Title,
			Description: description,
			Buttons:     buttons,
			URL:         e.Subject.URL,
			ObjectID:    objectID,
			EventID:     fmt.Sprintf("gitlab_mr_conflict:%s:%s:%s", objectID, p.SHA, username),
			OccurredAt:  e.OccurredAt,
		})
	}
	return out, nil
}

// conflictDescription says which branch the MR can no longer merge into, since
// the title alone does not tell the recipient what to rebase onto.
func conflictDescription(p ingestgitlab.ConflictPayload) string {
	if p.TargetBranch == "" {
		return "Cannot be merged: rebase or resolve the conflicts."
	}
	if p.SourceBranch == "" {
		return fmt.Sprintf("Cannot be merged into %s: rebase or resolve the conflicts.", p.TargetBranch)
	}
	return fmt.Sprintf("%s can no longer be merged into %s: rebase or resolve the conflicts.", p.SourceBranch, p.TargetBranch)
}

// conflictRecipients are the people who can act on a conflict: the author
// first, then the assignees, deduped. It mirrors outcomeRecipients but reads a
// different payload.
func conflictRecipients(p ingestgitlab.ConflictPayload) []string {
	out := make([]string, 0, 1+len(p.Assignees))
	if p.Author.Username != "" {
		out = append(out, p.Author.Username)
	}
	for _, username := range p.Assignees {
		if username != "" && !slices.Contains(out, username) {
			out = append(out, username)
		}
	}
	return out
}
