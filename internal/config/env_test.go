package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("SISYPHUS_CONFIG", writeConfig(t, `
database:
  dsn: postgres://file/db
gitlab:
  url: https://gitlab.example.com
  token: from-file
ingest:
  worker:
    concurrency: 2
`))
	t.Setenv("SISYPHUS_GITLAB_TOKEN", "from-env")
	t.Setenv("SISYPHUS_INGEST_WORKER_CONCURRENCY", "7")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "from-env", cfg.GitLab.Token)
	require.Equal(t, 7, cfg.Ingest.Worker.Concurrency)
}

// An empty variable is not a value: deploy's compose file passes every
// credential as ${SISYPHUS_X:-}, so it is present and empty in every container
// unless the operator filled it in. Letting that win would blank a token the
// config file sets literally.
func TestEmptyEnvDoesNotEraseFileValue(t *testing.T) {
	clearEnv(t)
	t.Setenv("SISYPHUS_CONFIG", writeConfig(t, `
database:
  dsn: postgres://file/db
gitlab:
  url: https://gitlab.example.com
  token: from-file
`))
	t.Setenv("SISYPHUS_GITLAB_TOKEN", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "from-file", cfg.GitLab.Token)
}
