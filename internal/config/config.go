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

	GitLabRoots string

	Jira JiraConfig

	OpenRouter OpenRouter
	Telegram   Telegram

	MCPAddr string
}

// JiraConfig holds Jira REST API configuration for ingestion.
type JiraConfig struct {
	BaseURL  string
	Email    string
	APIToken string
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

	GitLabRoots string `yaml:"gitlab_roots"`

	Jira fileJiraConfig `yaml:"jira"`

	OpenRouter fileOpenRouter `yaml:"openrouter"`
	Telegram   fileTelegram   `yaml:"telegram"`

	MCPAddr string `yaml:"mcp_addr"`
}

type fileJiraConfig struct {
	BaseURL  string `yaml:"base_url"`
	Email    string `yaml:"email"`
	APIToken Secret `yaml:"api_token"`
	PAT      Secret `yaml:"pat"`
	Projects string `yaml:"projects"`
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
	jiraPAT, err := c.Jira.PAT.Resolve(baseDir)
	if err != nil {
		return Config{}, errors.Wrap(err, "jira pat")
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

	return Config{
		HTTPAddr:         c.HTTPAddr,
		DatabaseDSN:      dsn,
		QdrantAddr:       c.QdrantAddr,
		QdrantCollection: c.QdrantCollection,
		OllamaURL:        c.OllamaURL,
		EmbedProvider:    c.EmbedProvider,
		EmbedModel:       c.EmbedModel,
		EmbedDim:         c.EmbedDim,
		GitLabRoots:      c.GitLabRoots,
		Jira: JiraConfig{
			BaseURL:  c.Jira.BaseURL,
			Email:    c.Jira.Email,
			APIToken: jiraToken,
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
		MCPAddr: c.MCPAddr,
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
