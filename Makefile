test:
	@./go.test.sh
.PHONY: test

coverage:
	@./go.coverage.sh
.PHONY: coverage

test_fast:
	go test ./...

# Integration tests (testcontainers-backed, needs a Docker daemon). Includes the
# cross-source search smoke test in internal/smoke.
test_integration:
	go test -tags integration ./...
.PHONY: test_integration

tidy:
	go mod tidy

lint:
	golangci-lint run --fix ./...

fmt:
	golangci-lint fmt ./...

codegen:
	go generate ./internal/ent/... && go generate ./internal/oas/...

# Diff the ent schema (source of truth) against internal/ent/migrate/migrations
# and write a new versioned SQL migration file. Requires NAME and a local
# Docker daemon (spins up a throwaway postgres container for the diff).
migrate-diff:
	go run ./internal/ent/migrate/gen $(NAME)

# Rewrite atlas.sum after hand-writing a migration file. No Docker needed.
migrate-hash:
	go run ./internal/ent/migrate/gen -hash

run:
	go run ./cmd/ssapi

run-bot:
	go run ./cmd/ssbot

run-agent:
	go run ./cmd/ssagent

ingest:
	go run ./cmd/ssingest all

ingest-git:
	go run ./cmd/ssingest git

ingest-gitlab:
	go run ./cmd/ssingest gitlab

ingest-jira:
	go run ./cmd/ssingest jira

ingest-telegram:
	go run ./cmd/ssingest telegram

ingest-serve:
	go run ./cmd/ssingest serve

# Reclaim vector points no chunk references. Dry run first: gc deletes.
gc-dry-run:
	go run ./cmd/ssingest gc --dry-run

gc:
	go run ./cmd/ssingest gc

# Rebind chunks whose vector point is keyed by the wrong ID. Dry run first.
repair-dry-run:
	go run ./cmd/ssingest repair --dry-run

repair:
	go run ./cmd/ssingest repair

.PHONY: lint fmt codegen migrate-diff migrate-hash run run-bot run-agent ingest ingest-git ingest-gitlab ingest-jira ingest-telegram ingest-serve gc gc-dry-run repair repair-dry-run
