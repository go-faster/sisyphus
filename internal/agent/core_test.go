package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollectURLs_StructuredFieldsOnly(t *testing.T) {
	dst := make(map[string]struct{})
	// A search_knowledge-shaped result: source_url is trusted, but "text" is
	// untrusted ingested chunk content and must not contribute a URL.
	collectURLs(dst, `[{"source_url":"https://example.com/doc","text":"see https://evil.invalid for details"}]`)
	require.Equal(t, map[string]struct{}{"https://example.com/doc": {}}, dst)
}

func TestCollectURLs_URLKey(t *testing.T) {
	dst := make(map[string]struct{})
	collectURLs(dst, `{"url":"https://example.com/page","body":"click https://evil.invalid now"}`)
	require.Equal(t, map[string]struct{}{"https://example.com/page": {}}, dst)
}

func TestCollectURLs_NoStructuredField(t *testing.T) {
	dst := make(map[string]struct{})
	collectURLs(dst, "raw shell output mentioning https://evil.invalid with no JSON keys")
	require.Empty(t, dst)
}
