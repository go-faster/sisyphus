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
	if err := e.DecodePayload(&p); err != nil {
		return nil, errors.Wrap(err, "decode alert payload")
	}

	return []notify.Event{{
		Source:      notify.SourceAlerts,
		Type:        typ,
		Title:       e.Subject.Title,
		Description: strings.TrimSpace(p.Annotations["description"]),
		Labels:      labels(p),
		Buttons:     buttons(p),
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

// buttons offers what an on-call reader actually clicks. A runbook is the
// first of them: it used to be pasted into the body as a bare URL, which is
// what made a two-line alert wrap into a paragraph of link text.
//
// Only annotation values are used, never anything from the alert's labels or
// description: an annotation is written by whoever authored the alerting
// rule, which is the same trust level as the rest of the deployment.
func buttons(p ingestalert.AlertPayload) []notify.Button {
	var out []notify.Button
	for _, k := range annotationKeys(p.Annotations, "runbook") {
		out = append(out, notify.Button{Text: "Runbook", URL: p.Annotations[k]})
	}
	for _, k := range annotationKeys(p.Annotations, "dashboard") {
		out = append(out, notify.Button{Text: "Dashboard", URL: p.Annotations[k]})
	}
	if p.ExternalURL != "" {
		out = append(out, notify.Button{Text: "Alertmanager", URL: p.ExternalURL})
	}
	return out
}

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
