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
		Source:     notify.SourceAlerts,
		Type:       typ,
		Title:      e.Subject.Title,
		Body:       body(p),
		URL:        e.Subject.URL,
		ObjectID:   e.Subject.ID,
		EventID:    e.ID,
		OccurredAt: e.OccurredAt,
	}}, nil
}

// body renders the description annotation plus a few identifying labels, one
// per line (notify.Lines, since a bare newline renders as a space).
func body(p ingestalert.AlertPayload) string {
	lines := []string{strings.TrimSpace(p.Annotations["description"])}

	var pairs []string
	for _, k := range labelsShown {
		if v := p.Labels[k]; v != "" {
			pairs = append(pairs, k+"="+v)
		}
	}
	lines = append(lines, strings.Join(pairs, " "))

	// A runbook is the one annotation an on-call reader acts on, so it earns
	// its own line even though the label block is deliberately short.
	for _, k := range runbookKeys(p.Annotations) {
		lines = append(lines, k+": "+p.Annotations[k])
	}
	return notify.Lines(lines...)
}

func runbookKeys(annotations map[string]string) []string {
	var keys []string
	for k := range annotations {
		if strings.Contains(strings.ToLower(k), "runbook") && annotations[k] != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
