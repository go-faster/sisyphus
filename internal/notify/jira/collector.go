// Package jira is the Jira source adapter for the notification gateway: a
// collector that turns Jira issues into canonical internal/event Events, and a
// Projector that fans those into per-recipient notify.Events. It reuses
// internal/ingest/jira's REST fetcher rather than talking to Jira directly.
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-faster/errors"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	"github.com/go-faster/sisyphus/internal/event"
	ingestjira "github.com/go-faster/sisyphus/internal/ingest/jira"
)

// Fetcher is the subset of *ingestjira.Fetcher the collector needs, kept as
// an interface so tests can inject a fake instead of hitting Jira.
type Fetcher interface {
	FetchIssues(ctx context.Context, opts ingestjira.FetchOptions, cursor ingestjira.Cursor) ([]chunkjira.Issue, ingestjira.Cursor, bool, error)
}

// state is the collector's cursor, JSON-encoded into the notify_jira SyncState
// row's last_cursor. It carries only the incremental bound: the per-issue seen
// assignee that used to live here is gone — the collector emits each issue's
// current assignee and the outbox DedupKey suppresses repeats.
type state struct {
	Cursor ingestjira.Cursor `json:"cursor"`
}

// IssuePayload is the source-typed body of an event.TypeIssueUpdated event: the
// issue's current assignee, which the Projector turns into a notification. Only
// this package (which produced it) decodes it.
type IssuePayload struct {
	AssigneeAccountID string `json:"assignee_account_id"`
	AssigneeDisplay   string `json:"assignee_display"`
}

// Collector implements notify.EventCollector for Jira.
type Collector struct {
	Fetcher  Fetcher
	Projects []string
}

func New(fetcher Fetcher, projects []string) *Collector {
	return &Collector{Fetcher: fetcher, Projects: projects}
}

func (c *Collector) Source() event.Source { return event.SourceJira }

func (c *Collector) Collect(ctx context.Context, cursor string) ([]event.Event, string, error) {
	var st state
	if cursor != "" {
		if err := json.Unmarshal([]byte(cursor), &st); err != nil {
			return nil, "", errors.Wrap(err, "decode jira notify cursor")
		}
	}

	opts := ingestjira.FetchOptions{Projects: c.Projects, PageSize: 100}
	cur := st.Cursor

	var events []event.Event
	for {
		issues, nextCursor, hasMore, err := c.Fetcher.FetchIssues(ctx, opts, cur)
		if err != nil {
			return nil, "", errors.Wrap(err, "fetch jira issues")
		}

		for _, iss := range issues {
			e := event.Event{
				ID:         fmt.Sprintf("jira_issue_update:%s:%s", iss.Key, iss.Updated.UTC().Format(time.RFC3339)),
				Source:     event.SourceJira,
				Type:       event.TypeIssueUpdated,
				Subject:    event.Ref{ID: iss.Key, URL: iss.WebURL, Title: fmt.Sprintf("%s: %s", iss.Key, iss.Title)},
				Actor:      event.Actor{Display: iss.Reporter},
				OccurredAt: iss.Updated,
			}
			e, err := e.WithPayload(IssuePayload{
				AssigneeAccountID: iss.AssigneeAccountID,
				AssigneeDisplay:   iss.Assignee,
			})
			if err != nil {
				return nil, "", errors.Wrap(err, "encode issue payload")
			}
			events = append(events, e)
		}

		cur = nextCursor
		if !hasMore {
			break
		}
	}

	st.Cursor = cur
	nextCursor, err := json.Marshal(st)
	if err != nil {
		return nil, "", errors.Wrap(err, "encode jira notify cursor")
	}

	return events, string(nextCursor), nil
}
