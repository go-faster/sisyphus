package jira

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var wikiNames = map[string]string{
	"557058:0d6c-4e": "Alice Smith",
}

func TestPlainText(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "just a comment", "just a comment"},
		{"empty", "", ""},

		{"heading", "h2. Deploy failed\nrolled back", "Deploy failed\nrolled back"},
		{"quote_line", "bq. see above", "see above"},
		{"rule", "before\n----\nafter", "before\n\nafter"},
		{"bullets", "* one\n** two\n# three\n- four", "• one\n• two\n• three\n• four"},
		{"table", "||env||status||\n|prod|down|", "env | status\nprod | down"},

		{"bold", "the *prod* cluster", "the prod cluster"},
		{"italic", "_maybe_ later", "maybe later"},
		{"two_effects", "*one* *two*", "one two"},
		{"date_kept", "shipped 2024-01-02, fine", "shipped 2024-01-02, fine"},
		{"identifier_kept", "field max_retry_count is unset", "field max_retry_count is unset"},
		{"mono", "run {{make test}} first", "run make test first"},

		{"color", "{color:#de350b}down{color} now", "down now"},
		{"panel", "{panel:title=Result}all good{panel}", "all good"},
		{"quote_macro", "{quote}he said{quote}", "he said"},
		{"brace_value_kept", "returned {status: 500} twice", "returned {status: 500} twice"},

		{"link_labeled", "see [the runbook|https://wiki/x] please", "see the runbook please"},
		{"link_bare", "see [https://wiki/x]", "see https://wiki/x"},
		{"link_smartcard", "[MR|https://gitlab/x|smart-card]", "MR"},
		{"image", "graph !cpu.png|thumbnail! attached", "graph  attached"},
		{"exclamations_kept", "Done! Shipped it!", "Done! Shipped it!"},

		{"mention_known", "[~accountid:557058:0d6c-4e] please look", "@Alice Smith please look"},
		{"mention_unknown_cloud", "[~accountid:557058:ffff] ping", "@someone ping"},
		{"mention_server", "[~jsmith] ping", "@jsmith ping"},

		{"hard_break", `line one\\line two`, "line one\nline two"},
		{"escaped", `a \*literal\* star`, "a *literal* star"},

		{
			"code_block_verbatim",
			"see:\n{code:java}\nif (*x*) { [~a] }\n{code}\nthat",
			"see:\n\nif (*x*) { [~a] }\n\nthat",
		},
		{"noformat", "{noformat}*raw*{noformat}", "*raw*"},
		{"code_unterminated", "{code}\n*kept*", "*kept*"},

		{
			"automation_comment",
			"h3. Linked issues\n" +
				"{color:#403294}Automation{color} linked [ABC-1|https://jira/browse/ABC-1] " +
				"and notified [~accountid:557058:0d6c-4e].",
			"Linked issues\nAutomation linked ABC-1 and notified @Alice Smith.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, plainText(tt.in, wikiNames))
		})
	}
}

// The renderer quotes the result as plain text, so no rule may leave behind
// the macro delimiters it was meant to remove.
func TestPlainText_LeavesNoMacroTokens(t *testing.T) {
	const in = "{color:#ff0000}{quote}[~accountid:x] saw {{it}} in [a|http://b]{quote}{color}"
	got := plainText(in, nil)
	for _, tok := range []string{"{color", "{quote", "{{", "}}", "[~", "|http"} {
		require.NotContains(t, got, tok)
	}
}

func FuzzPlainText(f *testing.F) {
	for _, seed := range []string{
		"h1. x", "* a\n* b", "{color:red}x{color}", "[a|http://b]", "[~accountid:1]",
		"{code}x{code}", "!img.png!", `\\`, "||a||b||", "{{m}}", "*a* *b*", "{noformat}",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		got := plainText(body, wikiNames)
		require.True(t, len(got) <= len(body)+utf8Slack(body),
			"conversion must not grow the body beyond its bullet and mention substitutions")
		require.Equal(t, strings.TrimSpace(got), got)
	})
}

// utf8Slack bounds the only rules that make the text longer: a list bullet
// ("* " becomes "• ", four bytes more) and a mention resolving to a display
// name.
func utf8Slack(body string) int {
	return 4*(strings.Count(body, "\n")+1) + len(body)*len("Alice Smith")
}
