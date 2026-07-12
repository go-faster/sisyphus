package bot

import (
	"strings"
	"testing"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func render(t *testing.T, md string) (string, []tg.MessageEntityClass) {
	t.Helper()
	var eb entity.Builder
	require.NoError(t, renderMarkdown(&eb, md))
	text, entities := eb.Complete()
	return strings.TrimRight(text, "\n"), entities
}

func TestRenderMarkdown_Bold(t *testing.T) {
	text, entities := render(t, "**Problem**: something broke")
	require.Equal(t, "Problem: something broke", text)
	require.Len(t, entities, 1)
	require.IsType(t, &tg.MessageEntityBold{}, entities[0])
}

func TestEscapeMarkdown_RoundTrip(t *testing.T) {
	// Escaped, this is one paragraph: soft line breaks between its lines
	// render as spaces (see mdRenderer.walkInline's SoftLineBreak handling),
	// same as it would for any other single-paragraph Markdown input.
	raw := "*bold* _italic_ [link](evil) `code` # heading\n- list\n> quote snake_case_ident"
	want := "*bold* _italic_ [link](evil) `code` # heading - list > quote snake_case_ident"
	text, entities := render(t, escapeMarkdown(raw))
	require.Equal(t, want, text)
	require.Empty(t, entities)
}

func TestRenderMarkdown_List(t *testing.T) {
	text, _ := render(t, "- first\n- second\n")
	require.Equal(t, "- first\n- second", text)
}

func TestRenderMarkdown_CodeSpanAndLink(t *testing.T) {
	text, entities := render(t, "run `kubectl get pods` or see [dashboard](https://example.com)")
	require.Equal(t, "run kubectl get pods or see dashboard", text)

	var (
		hasCode bool
		hasURL  bool
	)
	for _, e := range entities {
		switch e.(type) {
		case *tg.MessageEntityCode:
			hasCode = true
		case *tg.MessageEntityTextURL:
			hasURL = true
		}
	}
	require.True(t, hasCode, "expected a code entity")
	require.True(t, hasURL, "expected a text URL entity")
}

func TestRenderMarkdown_Table(t *testing.T) {
	text, entities := render(t, strings.Join([]string{
		"| From | To | Port |",
		"| --- | --- | --- |",
		"| management VLAN | clients VLAN | 443 |",
		"| ANY VLAN | vSphere API EP | 443 |",
	}, "\n"))

	require.Contains(t, text, "From            | To             | Port")
	require.Contains(t, text, "management VLAN | clients VLAN   | 443")
	require.Len(t, entities, 1)
	require.IsType(t, &tg.MessageEntityPre{}, entities[0])
}

func TestRenderMarkdown_FencedCode(t *testing.T) {
	text, entities := render(t, "```go\nfmt.Println(\"hi\")\n```")
	require.Equal(t, "fmt.Println(\"hi\")", text)
	require.Len(t, entities, 1)
	require.IsType(t, &tg.MessageEntityPre{}, entities[0])
}
