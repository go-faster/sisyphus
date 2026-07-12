package answer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestFilterButtons_EmptyAllowedURLsRejectsEverything(t *testing.T) {
	// A nil or empty allowedURLs must reject every button rather than
	// silently skip the constraint - there's no context to vet URLs against
	// (e.g. no seed results and nothing discovered mid-loop), so nothing is
	// "vetted" and no button should pass.
	buttons := []index.Link{{Text: "Doc", URL: "https://example.com/doc"}}

	require.Empty(t, filterButtons(buttons, nil))
	require.Empty(t, filterButtons(buttons, map[string]struct{}{}))
}

func TestFilterButtons_OnlyAllowedURLsPass(t *testing.T) {
	buttons := []index.Link{
		{Text: "Allowed", URL: "https://example.com/allowed"},
		{Text: "Not allowed", URL: "https://example.com/not-allowed"},
	}
	allowed := map[string]struct{}{"https://example.com/allowed": {}}

	got := filterButtons(buttons, allowed)
	require.Equal(t, []index.Link{{Text: "Allowed", URL: "https://example.com/allowed"}}, got)
}

func TestSanitizeButtons_NoAllowlistConstraint(t *testing.T) {
	buttons := []index.Link{
		{Text: "Doc", URL: "https://example.com/doc"},
		{Text: "  Dup  ", URL: "https://example.com/doc"},
		{Text: "Invalid", URL: "not-a-url"},
	}
	got := sanitizeButtons(buttons)
	require.Equal(t, []index.Link{{Text: "Doc", URL: "https://example.com/doc"}}, got)
}

func TestParseSubmitAnswer_DoesNotApplyAllowlist(t *testing.T) {
	// parseSubmitAnswer only sanitizes; the caller (loop.go) is responsible
	// for the final allowlist-constrained filterButtons pass.
	args := `{"answer":"hi","buttons":[{"text":"Doc","url":"https://example.com/doc"}]}`
	ans, err := parseSubmitAnswer(args)
	require.NoError(t, err)
	require.Equal(t, []index.Link{{Text: "Doc", URL: "https://example.com/doc"}}, ans.Links)
}
