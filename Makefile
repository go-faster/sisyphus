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

.PHONY: lint fmt codegen run ingest ingest-git ingest-gitlab ingest-jira ingest-telegram
