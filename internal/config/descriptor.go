package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"
	"github.com/go-faster/figureout/source/env"
	"github.com/go-faster/figureout/source/yaml"
)

// wire is the descriptor's root: the configuration consumers read, plus the
// credentials in the shape they are written in.
type wire struct {
	Config
	Secrets secrets
}

// describe declares the whole configuration. Sections live in descriptor_*.go.
//
// Every section is a figureout.Group rather than a nested descriptor, for two
// reasons: Config is deliberately flatter than config.yaml (database.dsn is
// Config.DatabaseDSN), and a former path named by figureout.MovedFrom resolves
// in the scope that declares the field — the deprecated top-level keys are only
// nameable from the root.
func describe(c *wire, s *figureout.Schema[wire]) {
	describeCore(c, s)
	describeAPI(c, s)
	describeGit(c, s)
	describeGitLab(c, s)
	describeJira(c, s)
	describeTelegram(c, s)
	describeAgent(c, s)
	describeContext(c, s)
	describeAlertmanager(c, s)
	describeIngest(c, s)
	describeNotify(c, s)
	describeProxies(c, s)
	describeFetch(c, s)

	// Collected while resolving, never configured.
	figureout.Ignore(s, &c.Warnings, figureout.Reason("output of Load, not input"))
}

// descriptor compiles the model once, on a path that can report the failure as
// an error instead of panicking in init.
var descriptor = sync.OnceValues(func() (*figureout.Descriptor[wire], error) {
	return figureout.Derive(describe)
})

// Load reads configuration from YAML. Set SISYPHUS_CONFIG to choose the config
// file path; otherwise ./config.yaml is used when it exists. Environment
// variables named SISYPHUS_<PATH> override the file; an empty one does not
// (see [setEnvironment]).
func Load() (Config, error) {
	d, err := descriptor()
	if err != nil {
		return Config{}, errors.Wrap(err, "config descriptor")
	}

	baseDir := "."
	sources := make([]figureout.Source, 0, 2)
	if path := configPath(); path != "" {
		sources = append(sources, yaml.File(path))
		baseDir = filepath.Dir(path)
	}
	sources = append(sources, env.Values(setEnvironment(), env.Prefix("SISYPHUS_")))

	w, report, err := d.Resolve(sources...)
	if err != nil {
		return Config{}, err
	}

	cfg, err := postResolve(w, report, baseDir)
	if err != nil {
		return Config{}, err
	}
	if cfg.DatabaseDSN == "" {
		return Config{}, errors.New("database.dsn is required")
	}
	return cfg, nil
}

// Schema returns the configuration's JSON Schema model, for generating one.
func Schema() (*figureout.Model, error) {
	d, err := descriptor()
	if err != nil {
		return nil, errors.Wrap(err, "config descriptor")
	}
	return d.Model(), nil
}

// setEnvironment is the process environment without the variables that are
// present but empty.
//
// An empty variable is not a value here, and must not erase one. deploy's
// compose file passes every credential as ${SISYPHUS_X:-}, so each one is
// present and empty in every container unless the operator filled it in — and a
// bare `env.Current` would let that blank a token the config file sets
// literally. Nothing in this configuration wants to be set to the empty string
// on purpose, so dropping them costs nothing and keeps the file authoritative
// for anything the environment does not actually carry.
func setEnvironment() map[string]string {
	environ := os.Environ()
	vars := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		vars[k] = v
	}
	return vars
}

func configPath() string {
	if path := os.Getenv("SISYPHUS_CONFIG"); path != "" {
		return path
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}
	return ""
}
