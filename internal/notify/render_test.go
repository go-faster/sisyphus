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
			name: "mr merged",
			event: Event{
				Type:  EventMRMerged,
				Actor: Actor{Display: "John Doe", URL: "https://gitlab.example.com/jdoe"},
				Title: "MR !42: Fix the thing",
				URL:   "https://gitlab.example.com/g/p/-/merge_requests/42",
			},
			want: "🎉 [John Doe](https://gitlab.example.com/jdoe) merged " +
				"[MR \\!42\\: Fix the thing](https://gitlab.example.com/g/p/-/merge_requests/42)",
		},
		{
			name: "mr approved",
			event: Event{
				Type:  EventMRApproved,
				Actor: Actor{Display: "John Doe", URL: "https://gitlab.example.com/jdoe"},
				Title: "MR !42: Fix the thing",
				URL:   "https://gitlab.example.com/g/p/-/merge_requests/42",
			},
			want: "✅ [John Doe](https://gitlab.example.com/jdoe) approved " +
				"[MR \\!42\\: Fix the thing](https://gitlab.example.com/g/p/-/merge_requests/42)",
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
			name: "mr commented",
			event: Event{
				Type:        EventMRCommented,
				Actor:       Actor{Display: "John Doe", URL: "https://gitlab.example.com/jdoe"},
				Title:       "MR !42: Fix the thing",
				URL:         "https://gitlab.example.com/g/p/-/merge_requests/42#note_7",
				Description: "needs a rebase",
			},
			want: "💬 [John Doe](https://gitlab.example.com/jdoe) commented on " +
				"[MR \\!42\\: Fix the thing](https://gitlab.example.com/g/p/-/merge_requests/42#note_7)" +
				"\n\nneeds a rebase",
		},
		{
			name: "issue mentioned",
			event: Event{
				Type:        EventIssueMentioned,
				Actor:       Actor{Display: "Jane"},
				Title:       "ABC-1: Broken",
				URL:         "https://jira.example.com/browse/ABC-1",
				Description: "[~bob] can you look?",
			},
			// The comment body is ingested content, so it is escaped like any
			// other plain-text field — its brackets must not become a link.
			want: "📣 **Jane** mentioned you on [ABC\\-1\\: Broken](https://jira.example.com/browse/ABC-1)" +
				"\n\n\\[\\~bob\\] can you look\\?",
		},
		{
			// A comment whose body was all whitespace still renders as the
			// one-liner rather than as a message with a dangling blank.
			name: "comment without body",
			event: Event{
				Type:  EventMRCommented,
				Actor: Actor{Display: "John Doe"},
				Title: "MR",
				URL:   "https://gitlab.example.com/g/p/-/merge_requests/42",
			},
			want: "💬 **John Doe** commented on [MR](https://gitlab.example.com/g/p/-/merge_requests/42)",
		},
		{
			// No actor at all: a conflict is caused by whoever moved the
			// target branch, whom GitLab does not name, so the message leads
			// with the state instead of inventing a "Someone did this".
			name: "mr conflict",
			event: Event{
				Type:        EventMRConflict,
				Title:       "MR !42: Fix the thing",
				URL:         "https://gitlab.example.com/g/p/-/merge_requests/42",
				Description: "feature can no longer be merged into main: rebase or resolve the conflicts.",
			},
			want: "⚠️ _Merge conflict:_ [MR \\!42\\: Fix the thing](https://gitlab.example.com/g/p/-/merge_requests/42)\n\n" +
				"feature can no longer be merged into main\\: rebase or resolve the conflicts\\.",
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
			// The name is plain even though the event carries a URL: an
			// alert offers that URL as a button instead.
			want: "🔥 _Firing:_ **HighErrorRate**\n\n" +
				"5xx above threshold\n\n" +
				"```\nseverity=critical\nservice=checkout\n```",
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
			want: "🔥 _Firing:_ **HighErrorRate**\n\n```\ncluster=prod\n```",
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
			// A bare newline is a CommonMark soft break, which the Telegram
			// renderer turns into a space — that collapsed multi-line bodies
			// onto one line. Lines in one paragraph get hard breaks.
			name: "body lines get hard breaks",
			event: Event{
				Type:  EventInvestigationCompleted,
				Title: "X",
				Body:  "Verdict: confirmed\n- restart the pod",
			},
			want: "🔍 _Investigation:_ **X**\n\nVerdict: confirmed" + LineBreak + "- restart the pod",
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
			// A backtick run in a label value would end the fence early, and
			// a backslash escape shows up literally inside one.
			name: "backtick in label value is dropped",
			event: Event{
				Type:   EventAlertFiring,
				Title:  "X",
				Labels: []Label{{Key: "instance", Value: "a`b"}},
			},
			want: "🔥 _Firing:_ **X**\n\n```\ninstance=ab\n```",
		},
		{
			name: "empty labels render no code block",
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
		// A single-label host resolves only inside the network that minted
		// it. Telegram rejects a button carrying one and drops the whole
		// message with it, which is how a batch of alerts went missing.
		{"container hostname", Button{Text: "Alertmanager", URL: "http://a9869748c05a:9093"}, false},
		{"localhost", Button{Text: "Local", URL: "http://localhost:8080/x"}, false},
		{"intranet short name", Button{Text: "Wiki", URL: "https://wiki/x"}, false},
		{"ipv4 literal", Button{Text: "Node", URL: "http://10.0.12.235:8080/x"}, true},
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
