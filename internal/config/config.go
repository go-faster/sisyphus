// Package config loads scpbot configuration from YAML.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"
)

// Config holds all runtime configuration.
type Config struct {
	HTTPAddr    string
	DatabaseDSN string

	QdrantAddr       string
	QdrantCollection string

	OllamaURL     string
	EmbedProvider string
	EmbedModel    string
	EmbedDim      int

	GitLab GitLabConfig

	Jira JiraConfig

	OpenRouter OpenRouter
	Telegram   Telegram
	Proxies    ProxyConfig

	MCPAddr string
}

// JiraConfig holds Jira REST API configuration for ingestion.
type JiraConfig struct {
	BaseURL  string
	Email    string
	Username string
	APIToken string
	Password string
	PAT      string
	Projects string
}

// OpenRouter holds configuration for the OpenRouter LLM API.
type OpenRouter struct {
	APIKey string
	Model  string
}

// Enabled reports whether OpenRouter is configured.
func (o OpenRouter) Enabled() bool { return o.APIKey != "" }

// Telegram holds gotd auth configuration (plan: user session + bot).
type Telegram struct {
	AppID         int
	AppHash       string
	BotToken      string
	SessionDir    string
	MonitorChats  string
	IngestSession string
}

type fileConfig struct {
	HTTPAddr    string `yaml:"http_addr"`
	DatabaseDSN Secret `yaml:"database_dsn"`

	QdrantAddr       string `yaml:"qdrant_addr"`
	QdrantCollection string `yaml:"qdrant_collection"`

	OllamaURL     string `yaml:"ollama_url"`
	EmbedProvider string `yaml:"embed_provider"`
	EmbedModel    string `yaml:"embed_model"`
	EmbedDim      int    `yaml:"embed_dim"`

	GitLab fileGitLabConfig `yaml:"gitlab"`

	Jira fileJiraConfig `yaml:"jira"`

	OpenRouter fileOpenRouter  `yaml:"openrouter"`
	Telegram   fileTelegram    `yaml:"telegram"`
	Proxies    fileProxyConfig `yaml:"proxies"`

	MCPAddr string `yaml:"mcp_addr"`
}

// ProxyConfig configures per-client HTTP proxies.
type ProxyConfig struct {
	GitLab     string
	Jira       string
	Ollama     string
	OpenRouter string
}

type fileProxyConfig struct {
	GitLab     Secret `yaml:"gitlab"`
	Jira       Secret `yaml:"jira"`
	Ollama     Secret `yaml:"ollama"`
	OpenRouter Secret `yaml:"openrouter"`
}

type fileJiraConfig struct {
	BaseURL  string `yaml:"base_url"`
	Email    string `yaml:"email"`
	Username string `yaml:"username"`
	APIToken Secret `yaml:"api_token"`
	Password Secret `yaml:"password"`
	PAT      Secret `yaml:"pat"`
	Projects string `yaml:"projects"`
}

// GitLabConfig configures GitLab repository ingestion.
type GitLabConfig struct {
	WorkDir string         `yaml:"work_dir"`
	Token   string         `yaml:"-"`
	Repos   []GitLabSource `yaml:"repos"`
}

type fileGitLabConfig struct {
	WorkDir string         `yaml:"work_dir"`
	Token   Secret         `yaml:"token"`
	Repos   []GitLabSource `yaml:"repos"`
}

// GitLabSource describes a GitLab repository to ingest.
type GitLabSource struct {
	Root    string `yaml:"root"`
	URL     string `yaml:"url"`
	Repo    string `yaml:"repo"`
	Branch  string `yaml:"branch"`
	BaseURL string `yaml:"base_url"`
}

type fileOpenRouter struct {
	APIKey Secret `yaml:"api_key"`
	Model  string `yaml:"model"`
}

type fileTelegram struct {
	AppID         int    `yaml:"app_id"`
	AppHash       Secret `yaml:"app_hash"`
	BotToken      Secret `yaml:"bot_token"`
	SessionDir    string `yaml:"session_dir"`
	MonitorChats  string `yaml:"monitor_chats"`
	IngestSession string `yaml:"ingest_session"`
}

// Secret describes a secret loaded from a literal value, environment variable,
// or file. Scalar YAML values are treated as literal values.
type Secret struct {
	Value string `yaml:"value"`
	Env   string `yaml:"env"`
	File  string `yaml:"file"`
}

// UnmarshalYAML accepts either a scalar secret value or a secret reference.
func (s *Secret) UnmarshalYAML(value *yaml.Node) error {
	var plain string
	if err := value.Decode(&plain); err == nil {
		s.Value = plain
		return nil
	}

	type secret Secret
	var ref secret
	if err := value.Decode(&ref); err != nil {
		return err
	}
	*s = Secret(ref)
	return nil
}

// Load reads configuration from YAML. Set SCPBOT_CONFIG to choose the config
// file path; otherwise ./config.yaml is used when it exists.
func Load() (Config, error) {
	fc := defaultConfig()
	baseDir := "."

	if path := configPath(); path != "" {
		if err := loadFile(path, &fc); err != nil {
			return Config{}, err
		}
		baseDir = filepath.Dir(path)
	}

	c, err := fc.resolve(baseDir)
	if err != nil {
		return Config{}, err
	}
	if c.DatabaseDSN == "" {
		return Config{}, errors.New("database_dsn is required")
	}
	return c, nil
}

func defaultConfig() fileConfig {
	return fileConfig{
		HTTPAddr:         ":8080",
		QdrantAddr:       "localhost:6334",
		QdrantCollection: "corp_chunks",
		OllamaURL:        "http://localhost:11434",
		EmbedProvider:    "ollama",
		EmbedModel:       "bge-m3",
		EmbedDim:         1024,
		MCPAddr:          ":8081",
		OpenRouter: fileOpenRouter{
			Model: "openai/gpt-4o-mini",
		},
		Telegram: fileTelegram{
			SessionDir: "./session",
		},
	}
}

func configPath() string {
	if path := os.Getenv("SCPBOT_CONFIG"); path != "" {
		return path
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}
	return ""
}

func loadFile(path string, c *fileConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.Wrap(err, "read config file")
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return errors.Wrap(err, "parse config file")
	}
	return nil
}

func (c fileConfig) resolve(baseDir string) (Config, error) {
	dsn, err := c.DatabaseDSN.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "database_dsn")
	}
	jiraToken, err := c.Jira.APIToken.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira api_token")
	}
	jiraPassword, err := c.Jira.Password.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira password")
	}
	jiraPAT, err := c.Jira.PAT.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira pat")
	}
	gitlabToken, err := c.GitLab.Token.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "gitlab token")
	}
	openRouterKey, err := c.OpenRouter.APIKey.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "openrouter api_key")
	}
	telegramAppHash, err := c.Telegram.AppHash.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "telegram app_hash")
	}
	telegramBotToken, err := c.Telegram.BotToken.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "telegram bot_token")
	}
	proxies, err := c.Proxies.resolve(baseDir)
	if err != nil {
		return Config{}, err
	}

	return Config{
		HTTPAddr:         c.HTTPAddr,
		DatabaseDSN:      dsn,
		QdrantAddr:       c.QdrantAddr,
		QdrantCollection: c.QdrantCollection,
		OllamaURL:        c.OllamaURL,
		EmbedProvider:    c.EmbedProvider,
		EmbedModel:       c.EmbedModel,
		EmbedDim:         c.EmbedDim,
		GitLab: GitLabConfig{
			WorkDir: c.GitLab.WorkDir,
			Token:   gitlabToken,
			Repos:   c.GitLab.Repos,
		},
		Jira: JiraConfig{
			BaseURL:  c.Jira.BaseURL,
			Email:    c.Jira.Email,
			Username: c.Jira.Username,
			APIToken: jiraToken,
			Password: jiraPassword,
			PAT:      jiraPAT,
			Projects: c.Jira.Projects,
		},
		OpenRouter: OpenRouter{
			APIKey: openRouterKey,
			Model:  c.OpenRouter.Model,
		},
		Telegram: Telegram{
			AppID:         c.Telegram.AppID,
			AppHash:       telegramAppHash,
			BotToken:      telegramBotToken,
			SessionDir:    c.Telegram.SessionDir,
			MonitorChats:  c.Telegram.MonitorChats,
			IngestSession: c.Telegram.IngestSession,
		},
		Proxies: proxies,
		MCPAddr: c.MCPAddr,
	}, nil
}

func (c fileProxyConfig) resolve(baseDir string) (ProxyConfig, error) {
	gitlab, err := c.GitLab.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy gitlab")
	}
	jira, err := c.Jira.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy jira")
	}
	ollama, err := c.Ollama.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy ollama")
	}
	openrouter, err := c.OpenRouter.Resolve(baseDir)
	if err != nil {
		return ProxyConfig{}, errors.Wrap(err, "proxy openrouter")
	}
	return ProxyConfig{
		GitLab:     gitlab,
		Jira:       jira,
		Ollama:     ollama,
		OpenRouter: openrouter,
	}, nil
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
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrap(err, "read secret file")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
