# sisyphus

Internal support/dev assistant that ingests operational knowledge into Postgres full-text search and Qdrant vectors, then answers questions through Telegram, HTTP, or MCP.

## What It Does

- Ingests Git docs/commits/tags, GitLab issues/MRs/releases, Jira issues, Telegram support threads, and curated local files.
- Stores normalized documents and chunks in Postgres, with vectors in Qdrant for hybrid retrieval.
- Answers `/context` questions from Telegram and exposes the same retrieval path through HTTP and MCP.
- Optionally runs `/investigate` through a separate agent service connected to MCP tools.

## Quick Start

```bash
cp deploy/config.example.yaml deploy/config.yaml
export SISYPHUS_CONFIG=deploy/config.yaml
export SISYPHUS_DATABASE_DSN='postgres://sisyphus:sisyphus@localhost:5432/sisyphus?sslmode=disable'
export SISYPHUS_API_AUTH_TOKEN='change-me'

docker compose -f deploy/docker-compose.yml up -d postgres qdrant ollama
docker compose -f deploy/docker-compose.yml exec ollama ollama pull bge-m3
go run ./cmd/ssapi
```

Run ingestion in another shell:

```bash
go run ./cmd/ssingest all
```

## Documentation

- [Setup](docs/setup.md) - local services, compose, API checks, and development commands.
- [Configuration](docs/configuration.md) - key services, config sections, auth, proxies, and runtime options.
- [Ingestion](docs/ingestion.md) - supported sources, commands, resets, cursors, and provider examples.
- [Webhooks and polling](docs/webhooks.md) - GitLab/Jira webhook endpoints, polling, reliability, and metrics.

## Development

```bash
make fmt
make test_fast
make lint
make codegen
```
