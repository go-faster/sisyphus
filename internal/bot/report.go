package bot

import (
	"strings"

	"github.com/gotd/td/telegram/message/entity"

	"github.com/go-faster/sisyphus/internal/agent"
)

// telegramMessageLimit is Telegram's max message length, in UTF-16 code
// units. See https://core.telegram.org/api/entities.
const telegramMessageLimit = 4096

var verdictLabels = map[agent.Verdict]string{
	agent.VerdictSolved:             "Solved",
	agent.VerdictKnownIssue:         "Known issue",
	agent.VerdictNeedsInvestigation: "Needs further investigation",
	agent.VerdictOutOfScope:         "Out of scope",
	agent.VerdictEscalate:           "Needs escalation",
}

func verdictLabel(v agent.Verdict) string {
	if label, ok := verdictLabels[v]; ok {
		return label
	}
	return string(v)
}

// reportMarkdown assembles a Report's fields into a single Markdown
// document for delivery. Sections with nothing to say are omitted rather
// than padded with filler.
func reportMarkdown(r agent.Report) string {
	var sb strings.Builder

	sb.WriteString("**Problem**: ")
	sb.WriteString(r.Problem)
	sb.WriteString("\n\n")

	if len(r.Steps) > 0 {
		sb.WriteString("**Steps**\n")
		for _, s := range r.Steps {
			sb.WriteString("- ")
			sb.WriteString(s)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("**Verdict**: ")
	sb.WriteString(verdictLabel(r.Verdict))
	sb.WriteString("\n\n")

	if r.Findings != "" {
		sb.WriteString(r.Findings)
		sb.WriteString("\n\n")
	}

	if len(r.Sources) > 0 {
		sb.WriteString("**Sources**\n")
		for _, s := range r.Sources {
			sb.WriteString("- ")
			sb.WriteString(s)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(r.Actions) > 0 {
		sb.WriteString("**Actions**\n")
		for _, a := range r.Actions {
			sb.WriteString("- ")
			sb.WriteString(a)
			sb.WriteString("\n")
		}
	}

	return strings.TrimSpace(sb.String())
}

// splitMarkdown splits md into chunks that each render under limit UTF-16
// code units, breaking on blank-line (paragraph/section) boundaries so each
// chunk stays independently valid Markdown. If a single paragraph alone
// exceeds limit, it is emitted as its own (over-limit) chunk rather than
// split mid-formatting.
func splitMarkdown(md string, limit int) []string {
	paragraphs := strings.Split(md, "\n\n")

	var (
		chunks  []string
		current strings.Builder
	)
	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
	}
	for _, p := range paragraphs {
		candidateLen := entity.ComputeLength(p)
		if current.Len() > 0 {
			candidateLen += entity.ComputeLength(current.String()) + entity.ComputeLength("\n\n")
		}
		if current.Len() > 0 && candidateLen > limit {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}
	flush()

	return chunks
}
