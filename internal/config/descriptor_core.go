package config

import "github.com/go-faster/figureout"

// describeCore declares the stores every binary talks to: Postgres, Qdrant,
// Ollama and the embedding model.
//
// Each of these used to be a top-level key, and the old spelling still resolves
// through figureout.MovedFrom: using it warns, and setting both spellings is an
// error rather than a precedence rule.
func describeCore(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "database", func(s *figureout.Schema[wire]) {
		secret(s, &c.Secrets.DatabaseDSN, "dsn", figureout.MovedFrom("database_dsn")).
			Doc("postgres connection string; required")
	})
	figureout.Ignore(s, &c.DatabaseDSN, figureout.Reason("materialized from database.dsn"))

	figureout.Group(s, "qdrant", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.QdrantAddr, "addr", figureout.MovedFrom("qdrant_addr")).
			ApplyDefault("localhost:6334")
		figureout.Value(s, &c.QdrantCollection, "collection", figureout.MovedFrom("qdrant_collection")).
			ApplyDefault("corp_chunks")
	})

	figureout.Group(s, "ollama", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.OllamaURL, "url", figureout.MovedFrom("ollama_url")).
			ApplyDefault("http://localhost:11434")
	})

	figureout.Group(s, "embed", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.EmbedProvider, "provider", figureout.MovedFrom("embed_provider")).
			ApplyDefault("ollama")
		figureout.Value(s, &c.EmbedModel, "model", figureout.MovedFrom("embed_model")).
			ApplyDefault("bge-m3")
		figureout.Value(s, &c.EmbedDim, "dim", figureout.MovedFrom("embed_dim")).
			ApplyDefault(1024).
			Doc("embedding dimension; must match the model and the Qdrant collection")
	})
}

// describeAPI declares the HTTP surfaces: ssapi's own server and the MCP one.
func describeAPI(c *wire, s *figureout.Schema[wire]) {
	figureout.Group(s, "api", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.API.HTTPAddr, "http_addr", figureout.MovedFrom("http_addr")).
			ApplyDefault(defaultHTTPAddr)
		figureout.Value(s, &c.API.BaseURL, "base_url").
			ApplyDefault("http://localhost:8080").
			Doc("where ssbot, ssmcp and ssagent reach ssapi")
		secret(s, &c.Secrets.APIAuthToken, "auth_token").
			Doc("shared bearer token; ssapi refuses to start without one")
	})
	figureout.Ignore(s, &c.API.AuthToken, figureout.Reason("materialized from api.auth_token"))

	figureout.Group(s, "mcp", func(s *figureout.Schema[wire]) {
		figureout.Value(s, &c.MCP.Addr, "addr", figureout.MovedFrom("mcp_addr")).
			ApplyDefault(defaultMCPAddr)
		secret(s, &c.Secrets.MCPAuthToken, "auth_token", figureout.MovedFrom("mcp_auth_token")).
			Doc("optional; empty serves /mcp unauthenticated")
	})
	figureout.Ignore(s, &c.MCP.AuthToken, figureout.Reason("materialized from mcp.auth_token"))

	figureout.Group(s, "openrouter", func(s *figureout.Schema[wire]) {
		secret(s, &c.Secrets.OpenRouterAPIKey, "api_key")
		figureout.Value(s, &c.OpenRouter.Model, "model").ApplyDefault("openai/gpt-4o-mini")
		figureout.Value(s, &c.OpenRouter.ReasoningEffort, "reasoning_effort").
			ApplyDefault("").
			Enum("", "low", "medium", "high")
	})
	figureout.Ignore(s, &c.OpenRouter.APIKey, figureout.Reason("materialized from openrouter.api_key"))
}
