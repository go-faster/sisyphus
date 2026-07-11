package fetch

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllowedMethods_Empty(t *testing.T) {
	methods, err := allowedMethods(nil)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{http.MethodGet: true}, methods)
}

func TestAllowedMethods_SingleValid(t *testing.T) {
	methods, err := allowedMethods([]string{http.MethodPost})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{http.MethodPost: true}, methods)
}

func TestAllowedMethods_Multiple(t *testing.T) {
	methods, err := allowedMethods([]string{http.MethodGet, http.MethodPost, http.MethodDelete})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodDelete: true,
	}, methods)
}

func TestAllowedMethods_CaseNormalization(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected map[string]bool
	}{
		{
			name:     "lowercase",
			input:    []string{"get", "post"},
			expected: map[string]bool{http.MethodGet: true, http.MethodPost: true},
		},
		{
			name:     "mixed_case",
			input:    []string{"Get", "PoSt"},
			expected: map[string]bool{http.MethodGet: true, http.MethodPost: true},
		},
		{
			name:     "uppercase",
			input:    []string{"GET", "POST"},
			expected: map[string]bool{http.MethodGet: true, http.MethodPost: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			methods, err := allowedMethods(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.expected, methods)
		})
	}
}

func TestAllowedMethods_WhitespaceTrimming(t *testing.T) {
	methods, err := allowedMethods([]string{"  GET  ", "\tPOST\t", " DELETE "})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodDelete: true,
	}, methods)
}

func TestAllowedMethods_UnsupportedMethod(t *testing.T) {
	_, err := allowedMethods([]string{http.MethodGet, "TRACE"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported method")
}

func TestAllowedMethods_UnknownMethod(t *testing.T) {
	_, err := allowedMethods([]string{"FOO"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported method")
}

func TestAllowedMethods_EmptyStringsAfterTrimming(t *testing.T) {
	// Slice with only empty/whitespace strings should fall back to default GET
	methods, err := allowedMethods([]string{"", "  ", "\t", "\n"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{http.MethodGet: true}, methods)
}

func TestAllowedMethods_MixedValidAndEmpty(t *testing.T) {
	// Mix of valid methods and whitespace-only strings
	methods, err := allowedMethods([]string{"GET", "  ", "POST"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{http.MethodGet: true, http.MethodPost: true}, methods)
}

func TestAllowedMethods_AllValidMethods(t *testing.T) {
	methods, err := allowedMethods([]string{
		http.MethodGet, http.MethodHead, http.MethodPost,
		http.MethodPut, http.MethodPatch, http.MethodDelete,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{
		http.MethodGet:    true,
		http.MethodHead:   true,
		http.MethodPost:   true,
		http.MethodPut:    true,
		http.MethodPatch:  true,
		http.MethodDelete: true,
	}, methods)
}

func TestMatchPattern_BasicGlobMatch(t *testing.T) {
	result := matchPattern("https://example.com/**", "https://example.com/path/to/file")
	require.True(t, result)
}

func TestMatchPattern_NonMatching(t *testing.T) {
	result := matchPattern("https://example.com/**", "https://other.com/path")
	require.False(t, result)
}

func TestMatchPattern_ExactMatch(t *testing.T) {
	result := matchPattern("https://example.com/exact", "https://example.com/exact")
	require.True(t, result)
}

func TestMatchPattern_WildcardSingleLevel(t *testing.T) {
	result := matchPattern("https://example.com/*/file", "https://example.com/foo/file")
	require.True(t, result)
}

func TestMatchPattern_WildcardDoesntMatchSlash(t *testing.T) {
	// Single * should not match / (/, unlike **)
	result := matchPattern("https://example.com/*/file", "https://example.com/foo/bar/file")
	require.False(t, result)
}

func TestMatchPattern_DoublestarRecursive(t *testing.T) {
	result := matchPattern("https://example.com/**", "https://example.com/foo/bar/baz")
	require.True(t, result)
}

func TestMatchPattern_MismatchedDomain(t *testing.T) {
	result := matchPattern("https://example.com/**", "https://attacker.com/example.com/file")
	require.False(t, result)
}

func TestMatchPattern_NoPatternError(t *testing.T) {
	// matchPattern should return false, not panic, even if doublestar.Match might have issues
	// Test with an invalid pattern if doublestar can error; otherwise just verify it returns bool
	result := matchPattern("**/[invalid", "https://example.com/test")
	// The function returns false on error from doublestar.Match
	require.False(t, result)
}
