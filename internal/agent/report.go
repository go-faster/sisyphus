package agent

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/go-faster/sisyphus/internal/index"
)

// maxReportLinks caps how many link buttons a report may carry, so a runaway
// model can't attach an unbounded keyboard.
const maxReportLinks = 6

// Verdict is the outcome of an investigation.
type Verdict string

const (
	// VerdictSolved means the investigation found and confirmed a concrete cause.
	VerdictSolved Verdict = "solved"
	// VerdictKnownIssue means the problem matches an already-tracked issue.
	VerdictKnownIssue Verdict = "known_issue"
	// VerdictNeedsInvestigation means the claim was confirmed but the cause wasn't
	// pinned down.
	VerdictNeedsInvestigation Verdict = "needs_investigation"
	// VerdictOutOfScope means the report isn't about a system we own/support.
	VerdictOutOfScope Verdict = "out_of_scope"
	// VerdictEscalate means the issue needs a human (e.g. paging, access, or a
	// decision the agent can't make).
	VerdictEscalate Verdict = "escalate"
)

// Report is the structured result an investigation submits via the
// submitReportTool.
type Report struct {
	Problem  string       `json:"problem"`
	Steps    []string     `json:"steps,omitempty"`
	Verdict  Verdict      `json:"verdict"`
	Findings string       `json:"findings,omitempty"`
	Sources  []string     `json:"sources,omitempty"`
	Actions  []string     `json:"actions,omitempty"`
	Links    []index.Link `json:"links,omitempty"`
	// Debug carries agent-loop diagnostics, populated only when
	// agent.show_debug_info is enabled (see internal/agent.Investigate).
	Debug *index.Debug `json:"debug,omitempty"`
}

// hasActionableVerdict reports whether v is a verdict that can legitimately
// carry concrete next steps: a confirmed cause/fix, or a specific pinpoint
// still worth checking. Out-of-scope reports and escalations never get
// actions attached here — an "escalate" is itself the action.
func (v Verdict) hasActionableVerdict() bool {
	switch v {
	case VerdictSolved, VerdictKnownIssue, VerdictNeedsInvestigation:
		return true
	default:
		return false
	}
}

// normalize enforces invariants the prompt asks for but that an LLM can't be
// fully trusted to honor: actions are dropped unless the verdict is one that
// can actually carry a concrete next step.
func (r Report) normalize() Report {
	if !r.Verdict.hasActionableVerdict() {
		r.Actions = nil
	}
	r.Links = validLinks(r.Links)
	return r
}

// validLinks drops links that aren't safe to render as buttons (bad/relative
// URLs, non-http(s) schemes, empty labels), deduplicates by URL, and caps the
// count at maxReportLinks.
func validLinks(links []index.Link) []index.Link {
	if len(links) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(links))
	var out []index.Link
	for _, l := range links {
		if !l.Valid() {
			continue
		}
		if _, ok := seen[l.URL]; ok {
			continue
		}
		seen[l.URL] = struct{}{}
		out = append(out, l)
		if len(out) >= maxReportLinks {
			break
		}
	}
	return out
}

// CharLen returns the total character (rune) count across all fields, used
// to decide whether a report needs to be asked to shorten itself. Counting
// runes rather than bytes matters for non-ASCII text (e.g. Cyrillic), where
// len() would count roughly double the actual character count.
func (r Report) CharLen() int {
	n := utf8.RuneCountInString(r.Problem) + utf8.RuneCountInString(r.Findings)
	for _, s := range r.Steps {
		n += utf8.RuneCountInString(s)
	}
	for _, s := range r.Sources {
		n += utf8.RuneCountInString(s)
	}
	for _, s := range r.Actions {
		n += utf8.RuneCountInString(s)
	}
	for _, l := range r.Links {
		n += utf8.RuneCountInString(l.Text) + utf8.RuneCountInString(l.URL)
	}
	return n
}

const submitReportToolName = "submit_report"

// submitReportTool is the tool the investigator must call to finish: it
// forces the model to produce a structured verdict instead of free-form
// prose, so callers can reliably tell solved/out-of-scope/etc. apart and
// enforce the actions-only-when-actionable rule.
func submitReportTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        submitReportToolName,
				Description: openai.String("Submit the final investigation report. Call this exactly once, when done — it ends the investigation."),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"problem": map[string]any{
							"type":        "string",
							"description": "One or two sentences restating what was reported.",
						},
						"steps": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "The investigation steps taken, one short line each.",
						},
						"verdict": map[string]any{
							"type": "string",
							"enum": []string{
								string(VerdictSolved),
								string(VerdictKnownIssue),
								string(VerdictNeedsInvestigation),
								string(VerdictOutOfScope),
								string(VerdictEscalate),
							},
							"description": "solved: confirmed cause/fix found. known_issue: matches an existing tracked report. " +
								"needs_investigation: claim confirmed but cause not pinned down. out_of_scope: not our " +
								"system/responsibility. escalate: needs a human to act (paging, access, a decision).",
						},
						"findings": map[string]any{
							"type":        "string",
							"description": "Concrete facts/results reached. Empty if verdict is out_of_scope.",
						},
						"sources": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "What was checked to reach the verdict (dashboards, tickets, logs). Omit if not useful.",
						},
						"actions": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
							"description": "Concrete next steps (a command, a silence, a rollback, who to page, a ticket to " +
								"file). Only include when there is something specific to do or check — a real command " +
								"or a precise pinpoint. Never include for out_of_scope. Omit entirely rather than " +
								"padding with vague suggestions.",
						},
						"links": map[string]any{
							"type": "array",
							"description": "Actionable links to attach as tappable buttons: a relevant Grafana dashboard, " +
								"the matching Jira/GitLab ticket, a runbook. Use ONLY absolute http(s) URLs you actually " +
								"obtained from tool results — never invent or guess a URL. Omit when there's nothing " +
								"concrete to link to.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"text": map[string]any{
										"type":        "string",
										"description": "Short button label, e.g. \"Grafana: API latency\" or \"JIRA-123\".",
									},
									"url": map[string]any{
										"type":        "string",
										"description": "Absolute http(s) URL from a tool result.",
									},
								},
								"required": []string{"text", "url"},
							},
						},
					},
					"required": []string{"problem", "verdict"},
				},
			},
		},
	}
}

// parseReport decodes a submit_report tool call's arguments into a Report.
func parseReport(argsJSON string) (Report, error) {
	var r Report
	if err := json.Unmarshal([]byte(argsJSON), &r); err != nil {
		return Report{}, errors.Wrap(err, "unmarshal report")
	}
	return r.normalize(), nil
}
