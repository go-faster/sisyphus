package notify

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultRenderer(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name: "mr assigned",
			event: Event{
				Type:  EventMRAssigned,
				Actor: Actor{Display: "John Doe", URL: "https://gitlab.example.com/jdoe"},
				Title: "MR !42: Fix the thing",
				URL:   "https://gitlab.example.com/g/p/-/merge_requests/42",
			},
			want: "🔀 [John Doe](https://gitlab.example.com/jdoe) assigned you to " +
				"[MR \\!42\\: Fix the thing](https://gitlab.example.com/g/p/-/merge_requests/42)",
		},
		{
			name: "review requested",
			event: Event{
				Type:  EventMRReviewRequested,
				Actor: Actor{Display: "John Doe"},
				Title: "MR",
				URL:   "https://gitlab.example.com/g/p/-/merge_requests/42",
			},
			// No profile URL: the actor is bold rather than a dead link.
			want: "👀 **John Doe** requested your review on [MR](https://gitlab.example.com/g/p/-/merge_requests/42)",
		},
		{
			name: "issue assigned",
			event: Event{
				Type:  EventIssueAssigned,
				Actor: Actor{Display: "Jane", URL: "https://jira.example.com/secure/ViewProfile.jspa?name=jane"},
				Title: "ABC-1: Broken",
				URL:   "https://jira.example.com/browse/ABC-1",
			},
			want: "📋 [Jane](https://jira.example.com/secure/ViewProfile.jspa?name=jane) assigned you " +
				"[ABC\\-1\\: Broken](https://jira.example.com/browse/ABC-1)",
		},
		{
			name:  "unknown actor",
			event: Event{Type: EventIssueAssigned, Title: "ABC-1", URL: "https://jira.example.com/browse/ABC-1"},
			want:  "📋 **Someone** assigned you [ABC\\-1](https://jira.example.com/browse/ABC-1)",
		},
		{
			name: "alert firing",
			event: Event{
				Type:        EventAlertFiring,
				Title:       "HighErrorRate",
				URL:         "https://prometheus.example.com/graph",
				Description: "5xx above threshold",
				Labels:      []Label{{Key: "severity", Value: "critical"}, {Key: "service", Value: "checkout"}},
			},
			want: "🔥 _Firing:_ **[HighErrorRate](https://prometheus.example.com/graph)**\n\n" +
				"5xx above threshold" + LineBreak + "`severity=critical service=checkout`",
		},
		{
			// Every optional field empty: the template's blank lines must
			// collapse instead of leaving a message with a ragged tail.
			name:  "alert resolved without detail",
			event: Event{Type: EventAlertResolved, Title: "HighErrorRate"},
			want:  "✅ _Resolved:_ **HighErrorRate**",
		},
		{
			name: "alert without url is not a link",
			event: Event{
				Type:   EventAlertFiring,
				Title:  "HighErrorRate",
				Labels: []Label{{Key: "cluster", Value: "prod"}},
			},
			want: "🔥 _Firing:_ **HighErrorRate**\n\n`cluster=prod`",
		},
		{
			name: "investigation",
			event: Event{
				Type:  EventInvestigationCompleted,
				Title: "HighErrorRate",
				URL:   "https://sisyphus.example.com/jobs/1",
				Body:  "Verdict: confirmed" + LineBreak + "- restart the pod",
			},
			want: "🔍 _Investigation:_ **[HighErrorRate](https://sisyphus.example.com/jobs/1)**\n\n" +
				"Verdict: confirmed" + LineBreak + "- restart the pod",
		},
		{
			// An event type this package does not render yet still has to
			// produce a sensible message rather than an empty one.
			name: "unknown event type",
			event: Event{
				Type:  EventType("something_new"),
				Actor: Actor{Display: "John"},
				Title: "Thing",
				URL:   "https://example.com/thing",
			},
			want: "🔔 **John** notified you about [Thing](https://example.com/thing)",
		},
		{
			// Titles come from ingested content: emphasis in one must not
			// bleed into the rest of the message.
			name: "markdown in title is escaped",
			event: Event{
				Type:  EventAlertFiring,
				Title: "*not bold* _not italic_",
			},
			want: `🔥 _Firing:_ **\*not bold\* \_not italic\_**`,
		},
		{
			// A backtick in a label value would end the code span early, and
			// a backslash escape shows up literally inside one.
			name: "backtick in label value is dropped",
			event: Event{
				Type:   EventAlertFiring,
				Title:  "X",
				Labels: []Label{{Key: "instance", Value: "a`b"}},
			},
			want: "🔥 _Firing:_ **X**\n\n`instance=ab`",
		},
		{
			name: "empty labels render no code line",
			event: Event{
				Type:        EventAlertFiring,
				Title:       "X",
				Description: "detail",
				Labels:      []Label{{Key: "severity", Value: ""}},
			},
			want: "🔥 _Firing:_ **X**\n\ndetail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DefaultRenderer{}.Render(tt.event)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestButtonValid(t *testing.T) {
	tests := []struct {
		name   string
		button Button
		want   bool
	}{
		{"ok", Button{Text: "Runbook", URL: "https://runbooks.example.com/x"}, true},
		{"http", Button{Text: "Runbook", URL: "http://runbooks.example.com/x"}, true},
		{"no text", Button{URL: "https://runbooks.example.com/x"}, false},
		{"blank text", Button{Text: "   ", URL: "https://runbooks.example.com/x"}, false},
		{"no url", Button{Text: "Runbook"}, false},
		{"relative url", Button{Text: "Runbook", URL: "/runbooks/x"}, false},
		{"no host", Button{Text: "Runbook", URL: "https://"}, false},
		// A Telegram URL button opens a browser: anything else is either
		// unclickable or a scheme nobody vetted.
		{"javascript", Button{Text: "Runbook", URL: "javascript:alert(1)"}, false},
		{"tg scheme", Button{Text: "Chat", URL: "tg://resolve?domain=x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.button.Valid())
		})
	}
}

func TestValidButtons(t *testing.T) {
	require.Nil(t, ValidButtons(nil))

	got := ValidButtons([]Button{
		{Text: " Runbook ", URL: "https://runbooks.example.com/x"},
		{Text: "Broken", URL: "not a url"},
		{Text: "Duplicate", URL: "https://runbooks.example.com/x"},
		{Text: "Dashboard", URL: "https://grafana.example.com/d/1"},
	})
	require.Equal(t, []Button{
		{Text: "Runbook", URL: "https://runbooks.example.com/x"},
		{Text: "Dashboard", URL: "https://grafana.example.com/d/1"},
	}, got)
}
