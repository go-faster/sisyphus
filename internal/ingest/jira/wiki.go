package jira

import (
	"regexp"
	"strings"
)

// Jira's REST v2 API returns every text field as wiki markup, and nothing
// downstream speaks it: a notification quotes the body as plain text, which
// the renderer then escapes. So "{color:#de350b}*down*{color}" reaches
// Telegram as literal punctuation, an attached image as "!graph.png|width=8!",
// and a mention as its raw account id.
//
// The conversion lives here, next to [Mentions], for the same reason that one
// does: only the source adapter knows this is wiki markup at all. It is
// applied to [Comment] — the notification-only projection — and not to what
// the chunker indexes, where the markup costs nothing.

// plainText renders wiki markup as the plain text a notification quotes.
// names resolves a mention's id to a display name (see [inline] for what an
// unresolved one becomes).
//
// It is deliberately conservative about text effects (see [inlineEffects]):
// leftover "*bold*" is mildly ugly, while eating the "-" out of a date is a
// changed message.
func plainText(body string, names map[string]string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")

	// Verbatim regions are split off first, by alternating over the {code} and
	// {noformat} tokens: both may open and close on one line or span many, and
	// their contents must survive every rule below untouched. An unterminated
	// one takes the rest of the body with it, which still beats dropping it.
	var (
		sb     strings.Builder
		pos    int
		inside bool
	)
	for _, m := range verbatimRe.FindAllStringIndex(body, -1) {
		sb.WriteString(segment(body[pos:m[0]], inside, names))
		inside = !inside
		pos = m[1]
	}
	sb.WriteString(segment(body[pos:], inside, names))
	return strings.TrimSpace(sb.String())
}

func segment(s string, verbatim bool, names map[string]string) string {
	if verbatim {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = inline(blockPrefix(line), names)
	}
	return strings.Join(lines, "\n")
}

var (
	verbatimRe = regexp.MustCompile(`\{(?:code|noformat)(?::[^}\n]*)?}`)

	headingRe = regexp.MustCompile(`^h[1-6]\.\s+`)
	quoteRe   = regexp.MustCompile(`^bq\.\s+`)
	// A list marker needs the trailing space: "*bold*" opens a line with the
	// same character and is not a bullet.
	listRe = regexp.MustCompile(`^\s*(?:[*#]+|-)\s+`)
)

// blockPrefix strips the markup that only means something at the start of a
// line: headings, quotes, list bullets and rules. Table rows become their
// cells joined by a pipe, since a chat cannot render a table anyway.
func blockPrefix(line string) string {
	t := strings.TrimSpace(line)
	switch {
	case t == "----":
		return ""
	case strings.HasPrefix(t, "|"):
		return tableRow(t)
	}
	if loc := headingRe.FindStringIndex(t); loc != nil {
		return t[loc[1]:]
	}
	if loc := quoteRe.FindStringIndex(t); loc != nil {
		return t[loc[1]:]
	}
	if loc := listRe.FindStringIndex(t); loc != nil {
		return "• " + t[loc[1]:]
	}
	return line
}

func tableRow(line string) string {
	line = strings.ReplaceAll(line, "||", "|")
	cells := make([]string, 0, 4)
	for c := range strings.SplitSeq(strings.Trim(line, "|"), "|") {
		if c = strings.TrimSpace(c); c != "" {
			cells = append(cells, c)
		}
	}
	return strings.Join(cells, " | ")
}

var (
	// linkRe matches "[text|url]" and "[url]". Jira also allows a third
	// pipe-separated part (a link title, or "smart-card" on Cloud); it is
	// swallowed by the second group and dropped with the URL.
	linkRe = regexp.MustCompile(`\[([^\[\]|\n]*)\|([^\[\]\n]*)]`)
	bareRe = regexp.MustCompile(`\[([^\[\]|\n]+)]`)
	// imageRe matches an embedded attachment. The filename may carry no
	// whitespace, which is what keeps "Done! Shipped!" from reading as one.
	imageRe = regexp.MustCompile(`!([^\s!|]+(?:\|[^!\n]*)?)!`)
	monoRe  = regexp.MustCompile(`\{\{(.*?)}}`)
	// macroRe strips a known macro's opening and closing token, keeping what
	// it wrapped. The names are listed rather than matched by shape so that a
	// brace-delimited value in the text ("{status: 500}") survives.
	macroRe = regexp.MustCompile(`\{(?:color|quote|panel|anchor|toc|tip|info|note|warning|section|column|expand|cite)(?::[^}\n]*)?}`)
	// hardBreak is wiki markup's forced line break.
	hardBreak = regexp.MustCompile(`\\\\`)
	escapeRe  = regexp.MustCompile(`\\([-*_+{}\[\]|!?~^])`)
)

func inline(s string, names map[string]string) string {
	s = hardBreak.ReplaceAllString(s, "\n")
	s = mentionRe.ReplaceAllStringFunc(s, func(m string) string {
		id := mentionRe.FindStringSubmatch(m)[1]
		if name := names[id]; name != "" {
			return "@" + name
		}
		// Only Cloud writes "accountid:", and only its ids are opaque hashes
		// worth hiding. A Server/DC mention is the username itself, which
		// reads fine unresolved. "Someone" is what the renderer calls an actor
		// it cannot name.
		if strings.Contains(m, "accountid:") {
			return "@someone"
		}
		return "@" + id
	})
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		g := linkRe.FindStringSubmatch(m)
		// The label is what a reader needs; the URL would eat the excerpt's
		// budget, and the notification already carries a button to the comment.
		if text := strings.TrimSpace(g[1]); text != "" {
			return text
		}
		return strings.TrimSpace(g[2])
	})
	s = bareRe.ReplaceAllString(s, "$1")
	s = imageRe.ReplaceAllString(s, "")
	s = monoRe.ReplaceAllString(s, "$1")
	s = macroRe.ReplaceAllString(s, "")
	s = inlineEffects(s)
	return escapeRe.ReplaceAllString(s, "$1")
}

// effectRe unwraps one text effect: the delimiter must open after whitespace
// or an opening bracket and close before whitespace or punctuation, which is
// what keeps "2024-01-01" and "a_b_c" intact.
//
// Subscript and superscript are deliberately absent: "~" and "^" appear in
// paths and versions far more often than they wrap a word, and an unconverted
// one costs a stray character where a wrong conversion costs a changed word.
var effectRe = regexp.MustCompile(`(^|[\s([{])([*_+-])([^\s].*?[^\s]|[^\s])([*_+-])($|[\s.,;:!?)\]}])`)

func inlineEffects(s string) string {
	// Repeated because one match consumes the whitespace the next one opens
	// on, so "*a* *b*" needs a second pass. Bounded rather than run to a fixed
	// point: each pass is a full scan, and no real comment nests these deeply.
	for range 4 {
		out := effectRe.ReplaceAllStringFunc(s, func(m string) string {
			g := effectRe.FindStringSubmatch(m)
			if g[2] != g[4] {
				return m
			}
			return g[1] + g[3] + g[5]
		})
		if out == s {
			return s
		}
		s = out
	}
	return s
}
