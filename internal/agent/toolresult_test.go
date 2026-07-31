package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestTruncateToolResult(t *testing.T) {
	for _, tt := range []struct {
		name      string
		in        string
		maxBytes  int
		truncated bool
		wantKept  string
	}{
		{name: "under limit", in: "hello", maxBytes: 10, truncated: false, wantKept: "hello"},
		{name: "exactly at limit", in: "hello", maxBytes: 5, truncated: false, wantKept: "hello"},
		{name: "over limit", in: "hello world", maxBytes: 5, truncated: true, wantKept: "hello"},
		{name: "empty", in: "", maxBytes: 5, truncated: false, wantKept: ""},
		{name: "zero limit disables", in: "hello", maxBytes: 0, truncated: false, wantKept: "hello"},
		{name: "negative limit disables", in: "hello", maxBytes: -1, truncated: false, wantKept: "hello"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := truncateToolResult(tt.in, tt.maxBytes)
			require.Equal(t, tt.truncated, truncated)
			if !truncated {
				require.Equal(t, tt.in, got)
				return
			}
			require.True(t, strings.HasPrefix(got, tt.wantKept))
			require.Contains(t, got, "[truncated:")
		})
	}
}

// A cut landing inside a multi-byte rune must not produce invalid UTF-8: the
// result is fed back to the model as a message, and an invalid sequence can be
// rejected or mangled by the transport before it ever gets there.
func TestTruncateToolResultRuneBoundary(t *testing.T) {
	// "日" is 3 bytes, so every limit from 1..8 lands mid-rune at least once.
	in := strings.Repeat("日", 3)
	require.Len(t, in, 9)

	for maxBytes := 1; maxBytes < len(in); maxBytes++ {
		got, truncated := truncateToolResult(in, maxBytes)
		require.True(t, truncated)
		require.True(t, utf8.ValidString(got), "limit %d produced invalid UTF-8", maxBytes)

		kept, _, found := strings.Cut(got, "\n[truncated:")
		require.True(t, found)
		require.LessOrEqual(t, len(kept), maxBytes, "kept more than the limit")
	}
}

func TestTruncateToolResultReportsSizes(t *testing.T) {
	in := strings.Repeat("a", 100)
	got, truncated := truncateToolResult(in, 10)
	require.True(t, truncated)
	require.Contains(t, got, "90 of 100 bytes omitted")
}

// coreLoop must extract URLs before truncating. A result whose JSON is cut in
// half parses as nothing, so collecting afterwards would silently drop every
// vetted source URL — the buttons guarantee depends on these being found.
func TestCollectURLsBeforeTruncation(t *testing.T) {
	body := strings.Repeat("x", 200)
	full := `[{"source_url":"https://example.com/a","text":"` + body + `"}]`

	fromFull := make(map[string]struct{})
	collectURLs(fromFull, full)
	require.Contains(t, fromFull, "https://example.com/a")

	cut, truncated := truncateToolResult(full, 60)
	require.True(t, truncated)

	fromTruncated := make(map[string]struct{})
	collectURLs(fromTruncated, cut)
	require.Empty(t, fromTruncated, "truncated JSON must not parse, proving order matters")
}
