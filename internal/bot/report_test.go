package bot

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/index"
)

func TestReportMarkdown_OmitsEmptySections(t *testing.T) {
	md := reportMarkdown(agent.Report{
		Problem: "db is slow",
		Verdict: agent.VerdictOutOfScope,
	})
	require.Contains(t, md, "**Problem**: db is slow")
	require.Contains(t, md, "**Verdict**: Out of scope")
	require.NotContains(t, md, "**Steps**")
	require.NotContains(t, md, "**Sources**")
	require.NotContains(t, md, "**Actions**")
}

func TestReportMarkdown_IncludesActionsWhenPresent(t *testing.T) {
	md := reportMarkdown(agent.Report{
		Problem: "disk full",
		Verdict: agent.VerdictSolved,
		Actions: []string{"run `df -h` and clear /var/log"},
	})
	require.Contains(t, md, "**Actions**")
	require.Contains(t, md, "run `df -h` and clear /var/log")
}

func TestReportMarkdown_IncludesDebugWhenPresent(t *testing.T) {
	md := reportMarkdown(agent.Report{
		Problem: "db is slow",
		Verdict: agent.VerdictSolved,
		Debug: &index.Debug{
			TraceID:    "abc123",
			DurationMS: 42,
			Iterations: 3,
			ToolCalls:  2,
		},
	})
	require.Contains(t, md, "**Debug**")
	require.Contains(t, md, "trace_id: `abc123`")
	require.Contains(t, md, "duration: 42ms")
	require.Contains(t, md, "iterations: 3")
	require.Contains(t, md, "tool_calls: 2")
}

func TestReportMarkdown_OmitsDebugWhenAbsent(t *testing.T) {
	md := reportMarkdown(agent.Report{Problem: "db is slow", Verdict: agent.VerdictSolved})
	require.NotContains(t, md, "**Debug**")
}

func TestSplitMarkdown_SingleChunkWhenUnderLimit(t *testing.T) {
	chunks := splitMarkdown("para one\n\npara two", 4096)
	require.Equal(t, []string{"para one\n\npara two"}, chunks)
}

func TestSplitMarkdown_SplitsOnParagraphBoundary(t *testing.T) {
	a := strings.Repeat("a", 30)
	b := strings.Repeat("b", 30)
	chunks := splitMarkdown(a+"\n\n"+b, 40)
	require.Equal(t, []string{a, b}, chunks)
}

func TestSplitMarkdown_OversizedParagraphStandsAlone(t *testing.T) {
	huge := strings.Repeat("x", 100)
	chunks := splitMarkdown(huge, 40)
	require.Equal(t, []string{huge}, chunks)
}
