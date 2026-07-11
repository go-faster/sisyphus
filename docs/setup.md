# Setup

This guide covers a local deployment for `ssapi`, `ssingest`, `ssbot`, `ssmcp`, and `ssagent`. See [configuration.md](configuration.md) for service/config details and [ingestion.md](ingestion.md) for source-specific examples.

## Prerequisites

- Go matching `go.mod`.
- Docker Compose for Postgres, Qdrant, Ollama, and service containers.
- A config file, usually `deploy/config.yaml` copied from `deploy/config.example.yaml`.
- An API bearer token in `SISYPHUS_API_AUTH_TOKEN`.

## Local Dependencies

Start the core backing services:

```bash
docker compose -f deploy/docker-compose.yml up -d postgres qdrant ollama
docker compose -f deploy/docker-compose.yml exec ollama ollama pull bge-m3
```

Run the API locally:

```bash
export SISYPHUS_CONFIG=deploy/config.yaml
export SISYPHUS_DATABASE_DSN='postgres://sisyphus:sisyphus@localhost:5432/sisyphus?sslmode=disable'
export SISYPHUS_API_AUTH_TOKEN='change-me'
go run ./cmd/ssapi
```

Or run the full compose stack:

```bash
docker compose -f deploy/docker-compose.yml up -d
```

`ssapi` listens on `:8080` inside the container and is published as `localhost:18079` by the compose file.

## Configuration

Copy the example config:

```bash
cp deploy/config.example.yaml deploy/config.yaml
```

Set required secrets through environment variables referenced by the config:

```bash
export SISYPHUS_DATABASE_DSN='postgres://sisyphus:sisyphus@localhost:5432/sisyphus?sslmode=disable'
export SISYPHUS_API_AUTH_TOKEN='change-me'
export SISYPHUS_GITLAB_TOKEN='glpat-...'
export SISYPHUS_JIRA_APITOKEN='...'
export SISYPHUS_OPENROUTER_API_KEY='sk-or-...'
```

`database_dsn` and `api.auth_token` are required for `ssapi`. Other secrets are only needed when their source or service is enabled.

## Ingestion

Run all configured sources:

```bash
go run ./cmd/ssingest all
```

Run one source:

```bash
go run ./cmd/ssingest git
go run ./cmd/ssingest gitlab
go run ./cmd/ssingest jira
go run ./cmd/ssingest telegram
```

Useful flags:

```bash
go run ./cmd/ssingest all --dry-run
go run ./cmd/ssingest gitlab --since 2026-01-01T00:00:00Z
go run ./cmd/ssingest jira --limit 50
go run ./cmd/ssingest all --reset all --yes-i-mean-all
```

Incremental state is stored in `sync_states`. Re-running ingestion is safe; unchanged documents are skipped by body hash.

See [ingestion.md](ingestion.md) for reset commands, source examples, cursors, and provider-specific behavior.

## API Check

Health is public:

```bash
curl http://localhost:18079/health
```

Search requires the API bearer token:

```bash
curl -sS http://localhost:18079/search \
  -H "Authorization: Bearer ${SISYPHUS_API_AUTH_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"query":"deployment runbook","limit":5}'
```

## Webhooks and Polling

Webhooks and polling are optional and live on `ssapi`. Both trigger regular incremental ingestion through the same debounced trigger; webhooks are event-driven, polling is timer-driven, and neither stores webhook events.

See [webhooks.md](webhooks.md) for provider setup, polling config, and endpoint details.

## Development Checks

```bash
make fmt
make test_fast
make lint
```
