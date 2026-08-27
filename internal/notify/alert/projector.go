// Package alert projects Alertmanager events straight into broadcast
// notifications, so a chat hears about an alert whether or not an agent
// investigates it.
//
// It is the cheap half of the alerting path: investigation.Projector announces
// what the agent concluded, minutes later and only when
// alertmanager.investigate is on, while this announces the transition itself
// the moment the webhook lands. Both address a chat rather than a user — an
// alert names no GitLab or Jira identity to deliver to.
package alert

import (
	"net/url"
	"sort"
	"strings"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
	ingestalert "github.com/go-faster/sisyphus/internal/ingest/alertmanager"
	"github.com/go-faster/sisyphus/internal/notify"
)

// labelsShown are the labels worth a line in a chat message, in this order.
// The full label set belongs in Alertmanager, not in a notification.
var labelsShown = []string{"severity", "service", "namespace", "cluster", "instance"}

// Projector implements notify.Projector for alertmanager events.
type Projector struct{}

func (Projector) Project(e event.Event) ([]notify.Event, error) {
	var typ notify.EventType
	switch e.Type {
	case event.TypeAlertFiring:
		typ = notify.EventAlertFiring
	case event.TypeAlertResolved:
		typ = notify.EventAlertResolved
	default:
		// Another destination's event reached us; not an error.
		return nil, nil
	}

	var p ingestalert.AlertPayload
	if err := e.DecodePayload(ingestalert.AlertPayloadVersion, &p); err != nil {
		return nil, errors.Wrap(err, "decode alert payload")
	}

	return []notify.Event{{
		Source:      notify.SourceAlerts,
		Type:        typ,
		Title:       e.Subject.Title,
		Description: strings.TrimSpace(p.Annotations["description"]),
		Labels:      labels(p),
		Buttons:     buttons(p, e.Subject.URL, typ == notify.EventAlertFiring),
		URL:         e.Subject.URL,
		ObjectID:    e.Subject.ID,
		EventID:     e.ID,
		OccurredAt:  e.OccurredAt,
	}}, nil
}

// labels picks the identifying labels worth a line in a chat message, in
// labelsShown order. The full label set belongs in Alertmanager.
func labels(p ingestalert.AlertPayload) []notify.Label {
	out := make([]notify.Label, 0, len(labelsShown))
	for _, k := range labelsShown {
		if v := p.Labels[k]; v != "" {
			out = append(out, notify.Label{Key: k, Value: v})
		}
	}
	return out
}

// buttons offers what an on-call reader actually clicks.
//
// The alert's own generator URL is one of them. It used to be the *title's*
// link instead, which put the rule that fired and the Alertmanager button
// side by side pointing at two different systems, with no way to tell which
// was which — one was underlined text, the other a button. Both are buttons
// now and the title is plain (see notify.templateData.TitleText).
//
// Only annotation values are used for the runbook and dashboard, never
// anything from the alert's labels or description: an annotation is written
// by whoever authored the alerting rule, which is the same trust level as the
// rest of the deployment. The generator URL is the third source at that
// level — Prometheus or vmalert composes it from its own external URL, and
// the alerting target it scraped never sees it. The last two are built here
// from Alertmanager's own external URL, so nothing outside the alerting stack
// decides where they point.
func buttons(p ingestalert.AlertPayload, generatorURL string, firing bool) []notify.Button {
	var out []notify.Button
	for _, k := range annotationKeys(p.Annotations, "runbook") {
		out = append(out, notify.Button{Text: "Runbook", URL: p.Annotations[k]})
	}
	for _, k := range annotationKeys(p.Annotations, "dashboard") {
		out = append(out, notify.Button{Text: "Dashboard", URL: p.Annotations[k]})
	}
	if generatorURL != "" {
		out = append(out, notify.Button{Text: "Rule", URL: generatorURL})
	}
	if base := strings.TrimRight(p.ExternalURL, "/"); base != "" {
		var filter string
		// An alert with none of the matcher labels leaves the parameter off
		// rather than sending "{}", which the UI would read as a filter
		// matching nothing.
		if f := matcherFilter(p.Labels); f != "{}" {
			filter = "?filter=" + url.QueryEscape(f)
		}
		out = append(out, notify.Button{Text: "Alertmanager", URL: base + "/#/alerts" + filter})
		// A resolved alert has nothing left to silence.
		if firing {
			out = append(out, notify.Button{Text: "Silence", URL: base + "/#/silences/new" + filter})
		}
	}
	return out
}

// matcherLabels identify one alert well enough to filter the alert list on it
// and to seed a silence with, in the order Alertmanager should show them.
//
// Deliberately not the full label set: a silence prefilled with every label
// an exporter attached is one nobody reads before confirming, and any value
// carrying a quote or a brace has to survive being pasted back through the
// matcher parser. Deliberately not alertname alone either — that silences
// every host a rule covers, which on a per-instance alert is never what the
// reader clicking from one host's notification meant.
var matcherLabels = []string{"alertname", "service", "instance"}

// matcherFilter renders the Alertmanager UI's filter expression,
// {alertname="X", service="y"}. The UI parses it for both the alert list and
// the new-silence form, so one string seeds both.
func matcherFilter(labels map[string]string) string {
	var b strings.Builder
	b.WriteByte('{')
	for _, k := range matcherLabels {
		v := labels[k]
		if v == "" {
			continue
		}
		if b.Len() > 1 {
			// No space after the comma: the filter rides the URL's
			// fragment, where a QueryEscaped space is a "+" that the UI
			// reads back as a literal one.
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(matcherEscaper.Replace(v))
		b.WriteString(`"`)
	}
	b.WriteByte('}')
	return b.String()
}

// matcherEscaper quotes what would otherwise end the matcher value early.
// Label values reach here from the alerting target, so a stray quote is a
// broken link and not a theoretical case.
var matcherEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// annotationKeys returns the annotations whose name contains substr, sorted
// so a rule with several (runbook_url, runbook_url_backup) renders the same
// way every time.
func annotationKeys(annotations map[string]string, substr string) []string {
	var keys []string
	for k, v := range annotations {
		if strings.Contains(strings.ToLower(k), substr) && v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
