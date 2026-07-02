test:
	@./go.test.sh
.PHONY: test

coverage:
	@./go.coverage.sh
.PHONY: coverage

test_fast:
	go test ./...

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

run:
	go run ./cmd/scpbot

ingest:
	go run ./cmd/scpingest all

ingest-git:
	go run ./cmd/scpingest git

ingest-gitlab:
	go run ./cmd/scpingest gitlab

ingest-jira:
	go run ./cmd/scpingest jira

ingest-telegram:
	go run ./cmd/scpingest telegram

.PHONY: lint fmt codegen migrate-diff run ingest ingest-git ingest-gitlab ingest-jira ingest-telegram
