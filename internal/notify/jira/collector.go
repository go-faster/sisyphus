// Package jira implements notify.EventCollector for Jira issue-assignment
// events, reusing internal/ingest/jira's REST fetcher rather than talking to
// Jira directly.
package jira

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-faster/errors"

	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	ingestjira "github.com/go-faster/sisyphus/internal/ingest/jira"
	"github.com/go-faster/sisyphus/internal/notify"
)

// Fetcher is the subset of *ingestjira.Fetcher the collector needs, kept as
// an interface so tests can inject a fake instead of hitting Jira.
type Fetcher interface {
	FetchIssues(ctx context.Context, opts ingestjira.FetchOptions, cursor ingestjira.Cursor) ([]chunkjira.Issue, ingestjira.Cursor, bool, error)
}

// state is the collector's cursor, JSON-encoded into the notify_jira
// SyncState row's last_cursor. Seen records each issue's last-observed
// assignee accountId so the collector fires only on a genuine assignment
// change, not on every poll of an unchanged issue.
type state struct {
	Cursor ingestjira.Cursor `json:"cursor"`
	Seen   map[string]string `json:"seen"` // issue key -> last seen assignee accountId
}

// Collector implements notify.EventCollector for Jira.
type Collector struct {
	Fetcher  Fetcher
	Projects []string
}

func New(fetcher Fetcher, projects []string) *Collector {
	return &Collector{Fetcher: fetcher, Projects: projects}
}

func (c *Collector) Source() notify.Source { return notify.SourceJira }

func (c *Collector) Collect(ctx context.Context, cursor string) ([]notify.Event, string, error) {
	var st state
	if cursor != "" {
		if err := json.Unmarshal([]byte(cursor), &st); err != nil {
			return nil, "", errors.Wrap(err, "decode jira notify cursor")
		}
	}
	if st.Seen == nil {
		st.Seen = make(map[string]string)
	}

	opts := ingestjira.FetchOptions{Projects: c.Projects, PageSize: 100}
	cur := st.Cursor

	var events []notify.Event
	for {
		issues, nextCursor, hasMore, err := c.Fetcher.FetchIssues(ctx, opts, cur)
		if err != nil {
			return nil, "", errors.Wrap(err, "fetch jira issues")
		}

		for _, iss := range issues {
			prevAssignee := st.Seen[iss.Key]
			if iss.AssigneeAccountID != "" && iss.AssigneeAccountID != prevAssignee {
				events = append(events, notify.Event{
					Source: notify.SourceJira,
					Type:   notify.EventIssueAssigned,
					Recipient: notify.Actor{
						Source:  notify.SourceJira,
						Key:     iss.AssigneeAccountID,
						Display: iss.Assignee,
					},
					Actor:      notify.Actor{Source: notify.SourceJira, Display: iss.Reporter},
					Title:      fmt.Sprintf("%s: %s", iss.Key, iss.Title),
					URL:        iss.WebURL,
					ObjectID:   iss.Key,
					EventID:    fmt.Sprintf("jira_assign:%s:%s", iss.Key, iss.AssigneeAccountID),
					OccurredAt: iss.Updated,
				})
			}
			st.Seen[iss.Key] = iss.AssigneeAccountID
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
