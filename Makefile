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
	go run ./cmd/ingest all

ingest-jira:
	go run ./cmd/ingest jira

ingest-gitlab:
	go run ./cmd/ingest gitlab

ingest-telegram:
	go run ./cmd/ingest telegram

.PHONY: lint fmt codegen run ingest ingest-jira ingest-gitlab ingest-telegram
