# internal/answer

Agentic `/context` answerer (`index.Answerer`). `AgenticAnswerer` runs `agent.coreLoop`
with in-process `search_knowledge`/`fetch_url` tools (`knowledge_tools.go`), merged via
`MultiToolSource` with an optional ssh-mcp-backed shell sandbox (`ssh_tools.go`, an MCP
client over streamable-http).

Enabled by `context.agentic: true` **and** OpenRouter being configured; otherwise
`wire.New` falls back to `internal/llm/openrouter.Answerer` (or a stub) — silently, with no
startup warning today if an operator sets `agentic: true` without OpenRouter.

## Button URLs

Same `submit_answer` / `filterButtons` contract as the non-agentic answerer, but the
allowed-URL set is built from **two** sources:

1. the seed results' `source_url` (`buildSeedMessages`), and
2. any URL the loop *discovers* while calling tools mid-conversation.

Discovery is `agent.collectURLs` — structured JSON keys only, never a scan of result text.
That restriction is what keeps a link merely mentioned inside untrusted ingested content
from becoming a clickable button. See `internal/agent/CLAUDE.md`.

## Sandbox availability is told to the model

When `context.ssh_mcp_url` is unset or unreachable, the `ssh_*` tools are unavailable and
`AgenticAnswerer` says so in its prompt (`AgenticOptions.SandboxEnabled`, wired from
`sshTools != nil` in `wire.go`) rather than silently letting `ssh_*` calls fail.

## pre_search

`pre_search`/`pre_search_limit` seed the loop with an initial retrieval. `AnswerRich` runs
it **only when the caller didn't already pass results** — `internal/api.Handler.Context`
always retrieves first, and a duplicate query there would also drop the caller's
service/source-tier filters.
