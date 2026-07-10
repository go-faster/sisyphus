package agent

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestParseReport_Links(t *testing.T) {
	t.Run("filters invalid and dedups", func(t *testing.T) {
		args := mustJSON(t, map[string]any{
			"problem": "p",
			"verdict": string(VerdictSolved),
			"links": []map[string]string{
				{"text": "Dashboard", "url": "https://grafana/d/1"},
				{"text": "no scheme", "url": "grafana/d/1"},
				{"text": "ftp", "url": "ftp://host/x"},
				{"text": "", "url": "https://grafana/d/2"},
				{"text": "dup", "url": "https://grafana/d/1"},
				{"text": "Ticket", "url": "https://jira/IDP-1"},
			},
		})
		r, err := parseReport(args)
		require.NoError(t, err)
		require.Equal(t, []index.Link{
			{Text: "Dashboard", URL: "https://grafana/d/1"},
			{Text: "Ticket", URL: "https://jira/IDP-1"},
		}, r.Links)
	})

	t.Run("caps at maxReportLinks", func(t *testing.T) {
		links := make([]map[string]string, 0, maxReportLinks+3)
		for i := range maxReportLinks + 3 {
			links = append(links, map[string]string{
				"text": "l",
				"url":  "https://host/" + string(rune('a'+i)),
			})
		}
		args := mustJSON(t, map[string]any{
			"problem": "p", "verdict": string(VerdictSolved), "links": links,
		})
		r, err := parseReport(args)
		require.NoError(t, err)
		require.Len(t, r.Links, maxReportLinks)
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestReport_CharLen(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want int
	}{
		{
			name: "empty",
			r:    Report{},
			want: 0,
		},
		{
			name: "ascii",
			r: Report{
				Problem:  "problem",  // 7
				Findings: "findings", // 8
				Steps:    []string{"step one", "step two"},
				Sources:  []string{"source"},
				Actions:  []string{"action"},
			},
			want: 7 + 8 + 8 + 8 + 6 + 6,
		},
		{
			name: "cyrillic counts runes, not bytes",
			r: Report{
				// Each Cyrillic rune is 2 bytes in UTF-8, so a byte-based
				// count would report double the true character count.
				Problem: "проблема", // 8 runes, 16 bytes
			},
			want: 8,
		},
		{
			name: "mixed ascii and cyrillic across fields",
			r: Report{
				Problem:  "issue: логин зависает", // ascii + cyrillic
				Findings: "найдено",
				Steps:    []string{"проверили базу знаний"},
				Sources:  []string{"jira IDP-944"},
				Actions:  []string{"kubectl logs"},
			},
			want: utf8.RuneCountInString("issue: логин зависает") +
				utf8.RuneCountInString("найдено") +
				utf8.RuneCountInString("проверили базу знаний") +
				utf8.RuneCountInString("jira IDP-944") +
				utf8.RuneCountInString("kubectl logs"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.r.CharLen())
		})
	}
}

// FuzzReport_CharLen checks that CharLen never diverges from summing
// utf8.RuneCountInString across all fields, for arbitrary input strings.
func FuzzReport_CharLen(f *testing.F) {
	seeds := []string{"", "a", "проблема", "mixed мир 世界", "🙂🙂🙂"}
	for _, s := range seeds {
		f.Add(s, s, s, s, s)
	}

	f.Fuzz(func(t *testing.T, problem, findings, step, source, action string) {
		r := Report{
			Problem:  problem,
			Findings: findings,
			Steps:    []string{step},
			Sources:  []string{source},
			Actions:  []string{action},
		}

		want := utf8.RuneCountInString(problem) +
			utf8.RuneCountInString(findings) +
			utf8.RuneCountInString(step) +
			utf8.RuneCountInString(source) +
			utf8.RuneCountInString(action)

		require.Equal(t, want, r.CharLen())
		// CharLen must never exceed the raw byte length (runes <= bytes for
		// any valid UTF-8 string), catching any accidental byte-counting
		// regression.
		require.LessOrEqual(t, r.CharLen(), len(problem)+len(findings)+len(step)+len(source)+len(action))
	})
}
