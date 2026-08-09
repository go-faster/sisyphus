package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A site with no patterns matches nothing, so it is dead configuration that
// reads like a working allowlist. url_patterns is Explicit for that reason.
func TestFetchSiteNeedsPatterns(t *testing.T) {
	clearEnv(t)
	t.Setenv("SISYPHUS_CONFIG", writeConfig(t, `
database:
  dsn: postgres://file/db
fetch:
  sites:
    - name: docs
`))

	_, err := Load()
	require.ErrorContains(t, err, "url_patterns")
}
