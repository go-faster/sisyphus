// Package gitlab implements notify.EventCollector for GitLab merge request
// assignment/review-request events, reusing internal/ingest/gitlab's REST
// fetcher rather than talking to GitLab directly.
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-faster/errors"

	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// Fetcher is the subset of *ingestgitlab.Fetcher the collector needs, kept
// as an interface so tests can inject a fake instead of hitting GitLab.
type Fetcher interface {
	FetchMergeRequests(ctx context.Context, page int, cursor ingestgitlab.Cursor) ([]ingestgitlab.MergeRequestRef, ingestgitlab.Cursor, bool, error)
}

// state is the collector's cursor, JSON-encoded into the notify_gitlab
// SyncState row's last_cursor. UpdatedAfter bounds which MRs are re-fetched;
// Seen records each MR's last-observed assignee/reviewer sets so the
// collector emits an event only for members newly added since the last poll
// (GitLab's API returns the current set, not a delta).
type state struct {
	UpdatedAfter string            `json:"updated_after"`
	Seen         map[string]seenMR `json:"seen"`
}

type seenMR struct {
	Assignees []string `json:"assignees"`
	Reviewers []string `json:"reviewers"`
}

// Collector implements notify.EventCollector for GitLab.
type Collector struct {
	Fetcher Fetcher
}

func New(fetcher Fetcher) *Collector {
	return &Collector{Fetcher: fetcher}
}

func (c *Collector) Source() notify.Source { return notify.SourceGitLab }

func (c *Collector) Collect(ctx context.Context, cursor string) ([]notify.Event, string, error) {
	var st state
	if cursor != "" {
		if err := json.Unmarshal([]byte(cursor), &st); err != nil {
			return nil, "", errors.Wrap(err, "decode gitlab notify cursor")
		}
	}
	if st.Seen == nil {
		st.Seen = make(map[string]seenMR)
	}

	startCursor := ingestgitlab.Cursor{UpdatedAfter: st.UpdatedAfter}
	maxObserved := st.UpdatedAfter

	var events []notify.Event
	page := 1
	for {
		refs, nextCursor, hasMore, err := c.Fetcher.FetchMergeRequests(ctx, page, startCursor)
		if err != nil {
			return nil, "", errors.Wrap(err, "fetch gitlab merge requests")
		}

		for _, ref := range refs {
			objectID := fmt.Sprintf("%s!%d", ref.Project, ref.MR.IID)
			prev := st.Seen[objectID]

			title := fmt.Sprintf("MR !%d: %s", ref.MR.IID, ref.MR.Title)
			actor := notify.Actor{Source: notify.SourceGitLab, Key: ref.MR.Author}

			for _, username := range newlyAdded(prev.Assignees, ref.MR.Assignees) {
				events = append(events, notify.Event{
					Source:     notify.SourceGitLab,
					Type:       notify.EventMRAssigned,
					Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
					Actor:      actor,
					Title:      title,
					URL:        ref.MR.WebURL,
					ObjectID:   objectID,
					EventID:    fmt.Sprintf("gitlab_mr_assign:%s:%s", objectID, username),
					OccurredAt: ref.MR.Updated,
				})
			}
			for _, username := range newlyAdded(prev.Reviewers, ref.MR.Reviewers) {
				events = append(events, notify.Event{
					Source:     notify.SourceGitLab,
					Type:       notify.EventMRReviewRequested,
					Recipient:  notify.Actor{Source: notify.SourceGitLab, Key: username},
					Actor:      actor,
					Title:      title,
					URL:        ref.MR.WebURL,
					ObjectID:   objectID,
					EventID:    fmt.Sprintf("gitlab_mr_review:%s:%s", objectID, username),
					OccurredAt: ref.MR.Updated,
				})
			}

			st.Seen[objectID] = seenMR{
				Assignees: append([]string(nil), ref.MR.Assignees...),
				Reviewers: append([]string(nil), ref.MR.Reviewers...),
			}
		}

		if nextCursor.UpdatedAfter > maxObserved {
			maxObserved = nextCursor.UpdatedAfter
		}
		if !hasMore {
			break
		}
		page++
	}

	st.UpdatedAfter = maxObserved
	nextCursor, err := json.Marshal(st)
	if err != nil {
		return nil, "", errors.Wrap(err, "encode gitlab notify cursor")
	}

	return events, string(nextCursor), nil
}

// newlyAdded returns the members of next not present in prev.
func newlyAdded(prev, next []string) []string {
	var added []string
	for _, n := range next {
		if !slices.Contains(prev, n) {
			added = append(added, n)
		}
	}
	return added
}
