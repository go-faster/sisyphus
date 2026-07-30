package agentstore

import (
	"context"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/event"
)

type fakeSubmitter struct {
	keys []string
	err  error
}

func (f *fakeSubmitter) SubmitEvent(_ context.Context, e event.Event, _ string) (Job, bool, error) {
	if f.err != nil {
		return Job{}, false, f.err
	}
	f.keys = append(f.keys, e.ID)
	return Job{}, true, nil
}

func alertEvent(typ event.Type, sev event.Severity) event.Event {
	return event.Event{
		ID:         "alertmanager:abc:" + string(typ),
		Source:     event.SourceAlertmanager,
		Type:       typ,
		Severity:   sev,
		Subject:    event.Ref{ID: "abc", Title: "HighErrorRate"},
		OccurredAt: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
	}
}

func TestSubscriberHandle(t *testing.T) {
	for _, tt := range []struct {
		name       string
		opts       SubscriberOptions
		e          event.Event
		wantSubmit bool
	}{
		{
			name:       "firing alert",
			e:          alertEvent(event.TypeAlertFiring, event.SeverityCritical),
			wantSubmit: true,
		},
		{
			name: "resolved alert is nothing to investigate",
			e:    alertEvent(event.TypeAlertResolved, event.SeverityCritical),
		},
		{
			name: "below severity floor",
			opts: SubscriberOptions{MinSeverity: event.SeverityCritical},
			e:    alertEvent(event.TypeAlertFiring, event.SeverityWarning),
		},
		{
			name:       "at severity floor",
			opts:       SubscriberOptions{MinSeverity: event.SeverityWarning},
			e:          alertEvent(event.TypeAlertFiring, event.SeverityWarning),
			wantSubmit: true,
		},
		{
			// A source that sets no severity has not said "unimportant";
			// a floor must not silently filter it out entirely.
			name:       "unset severity survives a floor",
			opts:       SubscriberOptions{MinSeverity: event.SeverityCritical},
			e:          alertEvent(event.TypeAlertFiring, ""),
			wantSubmit: true,
		},
		{
			name: "other event types are ignored",
			opts: SubscriberOptions{},
			e: event.Event{
				ID: "gitlab_mr_update:x!1", Source: event.SourceGitLab, Type: event.TypeMRUpdated,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSubmitter{}
			require.NoError(t, NewSubscriber(f, tt.opts).Handle(context.Background(), tt.e))
			if tt.wantSubmit {
				require.Equal(t, []string{tt.e.ID}, f.keys)
			} else {
				require.Empty(t, f.keys)
			}
		})
	}
}

// The submit key is the event ID, so a resent alert reuses the existing job
// rather than starting a second investigation of the same occurrence.
func TestSubscriberSubmitsByEventID(t *testing.T) {
	f := &fakeSubmitter{}
	s := NewSubscriber(f, SubscriberOptions{})
	e := alertEvent(event.TypeAlertFiring, event.SeverityCritical)

	require.NoError(t, s.Handle(context.Background(), e))
	require.NoError(t, s.Handle(context.Background(), e))
	require.Equal(t, []string{e.ID, e.ID}, f.keys)
}

func TestSubscriberSubmitError(t *testing.T) {
	f := &fakeSubmitter{err: errors.New("boom")}
	err := NewSubscriber(f, SubscriberOptions{}).Handle(context.Background(), alertEvent(event.TypeAlertFiring, ""))
	require.Error(t, err)
}

func TestDescribe(t *testing.T) {
	e := alertEvent(event.TypeAlertFiring, event.SeverityCritical)
	e.Subject.URL = "https://prometheus.example.com/graph"
	e.Attributes = map[string]string{"service": "checkout", "alertname": "HighErrorRate"}

	got := Describe(e)
	require.Contains(t, got, "alertmanager event: alert.firing — HighErrorRate")
	require.Contains(t, got, "Severity: critical")
	require.Contains(t, got, "https://prometheus.example.com/graph")
	// Attributes are sorted, so the same event always yields the same prompt.
	require.Contains(t, got, "- alertname: HighErrorRate\n- service: checkout\n")
}
