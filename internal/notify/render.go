package notify

// Message rendering: one text/template per event type, filled from an Event.
//
// Templates rather than string concatenation because the shape of a
// notification is a presentation decision that changes often (an emoji, a
// bold title, whether labels get their own line) while the data behind it —
// what a projector extracts from a source event — does not. Keeping the two
// apart means a wording change touches one const, not four projectors.
//
// The templates are Go constants, not configuration. An operator tuning
// notification wording is not a use case anyone has, and config-supplied
// templates would buy a parse-error-at-startup path plus a config, Helm and
// docs surface for it.

import (
	"strings"
	"text/template"

	"github.com/go-faster/errors"
)

// Templates are Telegram-flavored CommonMark, rendered to formatting
// entities by internal/bot.renderMarkdown. Note "**" for bold: a single "*"
// is CommonMark emphasis level 1, which that renderer maps to *italic*.
//
// Blank lines separate paragraphs; single newlines separate lines within one.
// normalize collapses whatever a template's empty fields leave behind, so a
// template need not guard each optional line with a {{if}}.
const (
	// assignTemplate is the one-liner for "someone did something to an object
	// that is now yours": who, what they did, and the object.
	assignTemplate = `{{.Emoji}} {{.Actor}} {{.Verb}} {{.Title}}`

	// commentTemplate is assignTemplate plus what was actually said: the
	// one-liner alone would make the recipient open the object to find out
	// whether the comment needs them.
	commentTemplate = `{{.Emoji}} {{.Actor}} {{.Verb}} {{.Title}}

{{.Description}}`

	// conflictTemplate leads with the state rather than with an actor: a
	// conflict is the one MR event nobody performed, so "X did Y" would have
	// to invent an X.
	conflictTemplate = `{{.Emoji}} _{{.Verb}}:_ {{.Title}}

{{.Description}}`

	// alertTemplate leads with the transition and the alert name, then the
	// description, then the identifying labels as a code block.
	alertTemplate = `{{.Emoji}} _{{.Verb}}:_ **{{.Title}}**

{{.Description}}

{{.Labels}}`

	// investigationTemplate is alertTemplate's shape with the agent's report
	// in place of a description.
	investigationTemplate = `{{.Emoji}} _{{.Verb}}:_ **{{.Title}}**

{{.Body}}`
)

// style is the per-event-type wording a template fills in: the emoji that
// makes an event type recognizable at a glance in a busy chat, and the verb
// phrase describing what happened.
type style struct {
	Emoji string
	Verb  string
	Tmpl  *template.Template
}

var (
	assign        = template.Must(template.New("assign").Parse(assignTemplate))
	comment       = template.Must(template.New("comment").Parse(commentTemplate))
	conflict      = template.Must(template.New("conflict").Parse(conflictTemplate))
	alert         = template.Must(template.New("alert").Parse(alertTemplate))
	investigation = template.Must(template.New("investigation").Parse(investigationTemplate))

	styles = map[EventType]style{
		EventMRAssigned:             {"🔀", "assigned you to", assign},
		EventMRReviewRequested:      {"👀", "requested your review on", assign},
		EventIssueAssigned:          {"📋", "assigned you", assign},
		EventMRCommented:            {"💬", "commented on", comment},
		EventIssueCommented:         {"💬", "commented on", comment},
		EventMRMerged:               {"🎉", "merged", assign},
		EventMRApproved:             {"✅", "approved", assign},
		EventMRThreadResolved:       {"☑️", "resolved a thread on", comment},
		EventMRConflict:             {"⚠️", "Merge conflict", conflict},
		EventMRMentioned:            {"📣", "mentioned you on", comment},
		EventIssueMentioned:         {"📣", "mentioned you on", comment},
		EventAlertFiring:            {"🔥", "Firing", alert},
		EventAlertResolved:          {"✅", "Resolved", alert},
		EventInvestigationCompleted: {"🔍", "Investigation", investigation},
	}

	// fallbackStyle renders an event type this package does not know about
	// yet. A new type reaching a chat as a vague-but-correct line beats it
	// reaching one as an empty message or an error.
	fallbackStyle = style{"🔔", "notified you about", assign}
)

// DefaultRenderer renders an Event through its event type's template.
type DefaultRenderer struct{}

// templateData is what a template sees. Every field is final Markdown: links
// are already composed and untrusted text is already escaped, so a template
// only decides placement and emphasis.
type templateData struct {
	Emoji string
	Verb  string
	// Title is the object the event is about, linked when it has a URL.
	Title string
	// Actor is who caused it, linked when the source carried a profile URL,
	// bold otherwise. "Someone" when the source named nobody.
	Actor string
	// Description is the plain-text lead paragraph, escaped.
	Description string
	// Body is projector-composed Markdown, passed through unescaped.
	Body string
	// Labels is the fenced code block of key=value pairs, or empty.
	Labels string
}

func (DefaultRenderer) Render(e Event) (string, error) {
	s, ok := styles[e.Type]
	if !ok {
		s = fallbackStyle
	}

	var sb strings.Builder
	if err := s.Tmpl.Execute(&sb, templateData{
		Emoji:       s.Emoji,
		Verb:        s.Verb,
		Title:       link(e.Title, e.URL),
		Actor:       actorText(e.Actor),
		Description: escapeMarkdown(e.Description),
		Body:        e.Body,
		Labels:      labelBlock(e.Labels),
	}); err != nil {
		return "", errors.Wrap(err, "execute template")
	}
	return normalize(sb.String()), nil
}

// link renders text as a Markdown link to url, or as escaped plain text when
// there is no URL to point at.
func link(text, url string) string {
	text = escapeMarkdown(strings.TrimSpace(text))
	if text == "" || strings.TrimSpace(url) == "" {
		return text
	}
	return "[" + text + "](" + strings.TrimSpace(url) + ")"
}

// actorText renders whoever caused the event: a link to their profile when
// the source carried one, their name in bold otherwise.
func actorText(a Actor) string {
	name := a.Display
	if name == "" {
		name = a.Key
	}
	if name == "" {
		name = "Someone"
	}
	if a.URL != "" {
		return link(name, a.URL)
	}
	return "**" + escapeMarkdown(name) + "**"
}

// labelBlock renders labels as a fenced code block, one "k=v" per line, or ""
// when there are none.
//
// A block rather than a single monospace line because these are meant to be
// copied — into a PromQL query, a kubectl invocation, a search box — and
// Telegram gives a code block its own copy affordance while a wrapped one-line
// span has to be selected by hand.
//
// Values are stripped of backticks rather than escaped: inside a code block a
// backslash escape shows up literally, and a run of backticks is the one thing
// that can end the fence early.
func labelBlock(labels []Label) string {
	pairs := make([]string, 0, len(labels))
	for _, l := range labels {
		if l.Key == "" || l.Value == "" {
			continue
		}
		pairs = append(pairs, strings.ReplaceAll(l.Key+"="+l.Value, "`", ""))
	}
	if len(pairs) == 0 {
		return ""
	}
	return codeFence + "\n" + strings.Join(pairs, "\n") + "\n" + codeFence
}

// codeFence opens and closes a fenced code block.
const codeFence = "```"

// normalize turns a template's output into the line structure Telegram
// renders correctly: blank lines separate paragraphs, lines within a
// paragraph get CommonMark hard breaks (LineBreak), and lines or paragraphs
// an empty field left blank disappear.
//
// This is why templates can interpolate optional fields on their own line
// without a guard: an absent description or label block collapses away here.
//
// A fenced code block is copied through verbatim — hard breaks would put two
// trailing spaces on every line of it, which is exactly the text someone is
// about to paste into a query.
func normalize(s string) string {
	var (
		out       []string // finished paragraphs
		paragraph []string // lines of the paragraph being built
		fence     []string // lines of the code block being built, nil outside one
	)
	flush := func() {
		if len(paragraph) > 0 {
			out = append(out, strings.Join(paragraph, LineBreak))
			paragraph = nil
		}
	}

	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		switch {
		case fence != nil:
			fence = append(fence, line)
			if strings.HasPrefix(strings.TrimSpace(line), codeFence) {
				out = append(out, strings.Join(fence, "\n"))
				fence = nil
			}
		case strings.HasPrefix(strings.TrimSpace(line), codeFence):
			flush()
			fence = []string{line}
		default:
			if line = strings.TrimRight(line, " \t"); line != "" {
				paragraph = append(paragraph, line)
			} else {
				flush()
			}
		}
	}
	// An unterminated fence is still better emitted than dropped.
	if len(fence) > 0 {
		out = append(out, strings.Join(fence, "\n"))
	}
	flush()
	return strings.Join(out, "\n\n")
}

// markdownSpecialChars are the ASCII punctuation characters CommonMark
// recognizes as backslash-escapable.
const markdownSpecialChars = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// escapeMarkdown backslash-escapes s so it renders as literal text. Titles,
// descriptions and names come from ingested content: a Jira summary with a
// stray "*" or a snake_case identifier otherwise bleeds emphasis into the
// rest of the message.
//
// internal/bot has the same function for the Markdown it renders. It is not
// shared: notify deliberately imports only stdlib, uuid and internal/event
// (see the package doc), and a sink-shaped dependency is exactly what that
// keeps out.
func escapeMarkdown(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(markdownSpecialChars, r) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
