package index

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// PublicURL asks one question: could this host resolve on someone else's
// device. It is applied where a link becomes a Telegram button, never to a
// Link in general — an intranet URL is a fine citation for a reader on the
// intranet.
func TestPublicURL(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
		want bool
	}{
		{"domain", "https://grafana.example.com/d/1", true},
		{"ipv4 literal", "http://10.0.12.235:8080/x", true},
		{"trailing dot", "https://example.com./x", true},
		// The shape that broke alert delivery in production: an alerting
		// stack naming its neighbor by container id.
		{"container id", "http://a9869748c05a:9093", false},
		{"localhost", "http://localhost:8080/x", false},
		{"intranet short name", "https://wiki/x", false},
		{"ipv6 literal", "http://[::1]:9093/x", false},
		{"no host", "https://", false},
		{"unparsable", "://nope", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, PublicURL(tt.url))
		})
	}
}

// Valid stays about the URL being an absolute http(s) link, so a citation to
// an intranet host survives it.
func TestLinkValidIgnoresReachability(t *testing.T) {
	require.True(t, Link{Text: "Wiki", URL: "https://wiki/x"}.Valid())
	require.False(t, Link{Text: "Wiki", URL: "/x"}.Valid())
}
