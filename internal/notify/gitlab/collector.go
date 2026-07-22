// Package gitlab is the GitLab source adapter for the notification gateway: a
// collector that turns GitLab merge requests into canonical internal/event
// Events, and a Projector that fans those into per-recipient notify.Events. It
// reuses internal/ingest/gitlab's REST fetcher rather than talking to GitLab
// directly.
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
)

// Fetcher is the subset of *ingestgitlab.Fetcher the collector needs, kept
// as an interface so tests can inject a fake instead of hitting GitLab.
type Fetcher interface {
	FetchMergeRequests(ctx context.Context, page int, cursor ingestgitlab.Cursor) ([]ingestgitlab.MergeRequestRef, ingestgitlab.Cursor, bool, error)
}

// state is the collector's cursor, JSON-encoded into the notify_gitlab
// SyncState row's last_cursor. It carries only the incremental bound: the
// per-MR assignee/reviewer diff that used to live here is gone — the collector
// emits each MR's current member set and the outbox DedupKey suppresses
// repeats.
type state struct {
	UpdatedAfter string `json:"updated_after"`
}

// MRPayload is the source-typed body of an event.TypeMRUpdated event: the
// current member sets the Projector fans out to. Only this package (which
// produced it) decodes it.
type MRPayload struct {
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

func (c *Collector) Source() event.Source { return event.SourceGitLab }

func (c *Collector) Collect(ctx context.Context, cursor string) ([]event.Event, string, error) {
	var st state
	if cursor != "" {
		if err := json.Unmarshal([]byte(cursor), &st); err != nil {
			return nil, "", errors.Wrap(err, "decode gitlab notify cursor")
		}
	}

	startCursor := ingestgitlab.Cursor{UpdatedAfter: st.UpdatedAfter}
	maxObserved := st.UpdatedAfter

	var events []event.Event
	page := 1
	for {
		refs, nextCursor, hasMore, err := c.Fetcher.FetchMergeRequests(ctx, page, startCursor)
		if err != nil {
			return nil, "", errors.Wrap(err, "fetch gitlab merge requests")
		}

		for _, ref := range refs {
			objectID := fmt.Sprintf("%s!%d", ref.Project, ref.MR.IID)
			e := event.Event{
				ID:         fmt.Sprintf("gitlab_mr_update:%s:%s", objectID, ref.MR.Updated.UTC().Format(time.RFC3339)),
				Source:     event.SourceGitLab,
				Type:       event.TypeMRUpdated,
				Subject:    event.Ref{ID: objectID, URL: ref.MR.WebURL, Title: fmt.Sprintf("MR !%d: %s", ref.MR.IID, ref.MR.Title)},
				Actor:      event.Actor{Key: ref.MR.Author},
				OccurredAt: ref.MR.Updated,
				Attributes: map[string]string{"project": ref.Project},
			}
			e, err := e.WithPayload(MRPayload{Assignees: ref.MR.Assignees, Reviewers: ref.MR.Reviewers})
			if err != nil {
				return nil, "", errors.Wrap(err, "encode mr payload")
			}
			events = append(events, e)
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
