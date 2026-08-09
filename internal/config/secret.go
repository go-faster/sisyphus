package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/figureout"
)

// Secret describes a secret loaded from a literal value, environment variable,
// or file. Scalar YAML values are treated as literal values.
//
// It is a wire shape, not a resolved one: [Secret.Resolve] materializes it into
// the plain string the exported Config fields hold.
type Secret struct {
	Value string `yaml:"value"`
	Env   string `yaml:"env"`
	File  string `yaml:"file"`
}

// secretDescriptor describes the object spelling of a secret. The scalar
// spelling is widened into Value by [figureout.ScalarOr].
var secretDescriptor = figureout.MustDerive(func(c *Secret, s *figureout.Schema[Secret]) {
	figureout.Value(s, &c.Value, "value", figureout.Secret()).
		Doc("literal secret value")
	figureout.Value(s, &c.Env, "env").
		Doc("name of the environment variable holding the value")
	figureout.Value(s, &c.File, "file").
		Doc("path to a file holding the value, relative to the config file")
})

// secret registers a secret field: a scalar, or {value|env|file}.
func secret[R any](s *figureout.Schema[R], field *Secret, name string, opts ...figureout.FieldOption) *figureout.ObjectField {
	return figureout.ScalarOr(s, field, name, secretDescriptor,
		func(v string) Secret { return Secret{Value: v} }, opts...)
}

// Resolve returns the configured secret value.
func (s Secret) Resolve(baseDir string) (string, error) {
	set := 0
	if s.Value != "" {
		set++
	}
	if s.Env != "" {
		set++
	}
	if s.File != "" {
		set++
	}
	if set == 0 {
		return "", nil
	}
	if set > 1 {
		return "", errors.New("set only one of value, env, or file")
	}
	if s.Value != "" {
		return s.Value, nil
	}
	if s.Env != "" {
		return os.Getenv(s.Env), nil
	}

	path := s.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", errors.Wrap(err, "read secret file")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// secrets holds every credential in its wire shape. The resolved Config holds
// plain strings, so these live beside it rather than in it: figureout binds by
// pointer and every pointer must be inside the descriptor's root type.
type secrets struct {
	DatabaseDSN  Secret
	APIAuthToken Secret
	MCPAuthToken Secret

	GitToken            Secret
	GitLabToken         Secret
	GitLabWebhookSecret Secret

	JiraUsername      Secret
	JiraAPIToken      Secret
	JiraPassword      Secret
	JiraPAT           Secret
	JiraWebhookSecret Secret

	OpenRouterAPIKey Secret
	TelegramAppHash  Secret
	TelegramBotToken Secret

	AgentAuthToken        Secret
	AlertmanagerWebhookTk Secret

	ProxyFetch      Secret
	ProxyGit        Secret
	ProxyGitLab     Secret
	ProxyJira       Secret
	ProxyOllama     Secret
	ProxyOpenRouter Secret
}

// materialize resolves every secret into the Config field that holds it.
func (s *secrets) materialize(c *Config, baseDir string) error {
	for _, m := range []struct {
		what string
		from Secret
		into *string
	}{
		{"database dsn", s.DatabaseDSN, &c.DatabaseDSN},
		{"api auth_token", s.APIAuthToken, &c.API.AuthToken},
		{"mcp auth_token", s.MCPAuthToken, &c.MCP.AuthToken},
		{"git token", s.GitToken, &c.Git.Token},
		{"gitlab token", s.GitLabToken, &c.GitLab.Token},
		{"gitlab webhook secret", s.GitLabWebhookSecret, &c.GitLab.WebhookSecret},
		{"jira username", s.JiraUsername, &c.Jira.Username},
		{"jira api_token", s.JiraAPIToken, &c.Jira.APIToken},
		{"jira password", s.JiraPassword, &c.Jira.Password},
		{"jira pat", s.JiraPAT, &c.Jira.PAT},
		{"jira webhook secret", s.JiraWebhookSecret, &c.Jira.WebhookSecret},
		{"openrouter api_key", s.OpenRouterAPIKey, &c.OpenRouter.APIKey},
		{"telegram app_hash", s.TelegramAppHash, &c.Telegram.AppHash},
		{"telegram bot_token", s.TelegramBotToken, &c.Telegram.BotToken},
		{"agent auth_token", s.AgentAuthToken, &c.Agent.AuthToken},
		{"alertmanager webhook token", s.AlertmanagerWebhookTk, &c.Alertmanager.WebhookToken},
		{"proxy fetch", s.ProxyFetch, &c.Proxies.Fetch},
		{"proxy git", s.ProxyGit, &c.Proxies.Git},
		{"proxy gitlab", s.ProxyGitLab, &c.Proxies.GitLab},
		{"proxy jira", s.ProxyJira, &c.Proxies.Jira},
		{"proxy ollama", s.ProxyOllama, &c.Proxies.Ollama},
		{"proxy openrouter", s.ProxyOpenRouter, &c.Proxies.OpenRouter},
	} {
		v, err := m.from.Resolve(baseDir)
		if err != nil {
			return errors.Wrap(err, m.what)
		}
		*m.into = v
	}
	return nil
}
