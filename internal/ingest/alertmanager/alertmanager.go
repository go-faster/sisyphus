// Package alertmanager is the Alertmanager source adapter: it turns one
// Alertmanager webhook POST into canonical internal/event Events, one per
// alert in the group.
//
// Unlike the GitLab and Jira adapters it fetches nothing — Alertmanager
// pushes. It also produces no index.Document: an alert is an occurrence to
// react to, not knowledge to index. The destinations that care are the agent
// (investigate a firing alert) and, once the investigation reports back, the
// notification gateway.
package alertmanager

import (
	"encoding/json"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/event"
)

// labelAlertname is Alertmanager's canonical alert-name label.
const labelAlertname = "alertname"

// labelSeverity is the de-facto severity label. It is not part of the
// Alertmanager schema, but every common ruleset sets it, and it is what maps
// onto event.Severity so destinations can route on urgency without decoding
// the payload.
const labelSeverity = "severity"

// Webhook is the Alertmanager webhook body (config.WebhookConfig, schema
// version 4). Only the fields this adapter reads are modeled.
type Webhook struct {
	Version     string  `json:"version"`
	GroupKey    string  `json:"groupKey"`
	Receiver    string  `json:"receiver"`
	Status      string  `json:"status"`
	ExternalURL string  `json:"externalURL"`
	Alerts      []Alert `json:"alerts"`
}

// Alert is one alert inside a [Webhook].
type Alert struct {
	Status       string            `json:"status"` // "firing" or "resolved"
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// AlertPayload is the source-typed body of an alert event: everything a
// destination that understands Alertmanager may want, and nothing the
// envelope already carries generically.
type AlertPayload struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"starts_at"`
	EndsAt      time.Time         `json:"ends_at"`
	Receiver    string            `json:"receiver"`
	ExternalURL string            `json:"external_url"`
	GroupKey    string            `json:"group_key"`
}

// EventsFromWebhook decodes an Alertmanager webhook body into one event per
// alert.
func EventsFromWebhook(body []byte) ([]event.Event, error) {
	var hook Webhook
	if err := json.Unmarshal(body, &hook); err != nil {
		return nil, errors.Wrap(err, "decode alertmanager webhook")
	}

	events := make([]event.Event, 0, len(hook.Alerts))
	for _, alert := range hook.Alerts {
		e, err := eventFromAlert(hook, alert)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

func eventFromAlert(hook Webhook, alert Alert) (event.Event, error) {
	resolved := strings.EqualFold(alert.Status, "resolved")

	typ := event.TypeAlertFiring
	occurredAt := alert.StartsAt
	if resolved {
		typ = event.TypeAlertResolved
		occurredAt = alert.EndsAt
	}

	// Firing and resolved are distinct occurrences of the same subject, and
	// Alertmanager re-sends a firing alert every repeat_interval — so the id
	// pins the transition, not the delivery. A destination that already
	// investigated this firing must see the resend as the same occurrence.
	fingerprint := alert.Fingerprint
	if fingerprint == "" {
		fingerprint = labelFingerprint(alert.Labels)
	}
	id := "alertmanager:" + fingerprint + ":" + string(typ) + ":" + occurredAt.UTC().Format(time.RFC3339)

	attrs := make(map[string]string, len(alert.Labels)+1)
	maps.Copy(attrs, alert.Labels)
	if hook.Receiver != "" {
		attrs["receiver"] = hook.Receiver
	}

	e := event.Event{
		ID:         id,
		Source:     event.SourceAlertmanager,
		Type:       typ,
		Subject:    event.Ref{ID: fingerprint, URL: alert.GeneratorURL, Title: alertTitle(alert)},
		Severity:   severityFromLabels(alert.Labels),
		OccurredAt: occurredAt,
		Attributes: attrs,
	}
	e, err := e.WithPayload(AlertPayload{
		Labels:      alert.Labels,
		Annotations: alert.Annotations,
		StartsAt:    alert.StartsAt,
		EndsAt:      alert.EndsAt,
		Receiver:    hook.Receiver,
		ExternalURL: hook.ExternalURL,
		GroupKey:    hook.GroupKey,
	})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode alert payload")
	}
	return e, nil
}

// alertTitle renders the human-readable one-liner: the alert name, plus the
// summary annotation when the ruleset provides one.
func alertTitle(alert Alert) string {
	name := alert.Labels[labelAlertname]
	if name == "" {
		name = "alert"
	}
	if summary := alert.Annotations["summary"]; summary != "" {
		return name + ": " + summary
	}
	return name
}

func severityFromLabels(labels map[string]string) event.Severity {
	switch strings.ToLower(labels[labelSeverity]) {
	case "critical", "fatal", "page":
		return event.SeverityCritical
	case "warning", "warn", "major", "minor":
		return event.SeverityWarning
	case "info", "informational", "none":
		return event.SeverityInfo
	default:
		return ""
	}
}

// labelFingerprint stands in for Alertmanager's own fingerprint when a sender
// omits it: the label set identifies the alert, so the sorted label pairs
// identify it just as stably.
func labelFingerprint(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}
