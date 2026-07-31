# internal/answer

Agentic `/context` answerer (`index.Answerer`). `AgenticAnswerer` runs `agent.coreLoop`
with in-process `search_knowledge`/`fetch_url` tools (`knowledge_tools.go`), merged via
`MultiToolSource` with an optional ssh-mcp-backed shell sandbox (`ssh_tools.go`, an MCP
client over streamable-http).

Enabled by `context.agentic: true` **and** OpenRouter being configured; otherwise
`wire.New` falls back to `internal/llm/openrouter.Answerer` (or a stub) — silently, with no
startup warning today if an operator sets `agentic: true` without OpenRouter.

## search_knowledge returns snippets; get_chunks returns bodies

A search result carries each chunk's **full text**, so the result count is the dominant
term in its size — measured against a live corpus, `limit=30` returned ~133KB in a single
call, and the loop re-sends every result on every later iteration. So `search_knowledge`
returns a snippet plus `chunk_id` and `text_bytes`, and the model calls `get_chunks` for
the few bodies it actually wants (`chunk_tools.go`). Recall stays cheap; only what gets
read costs context.

**Summary mode is conditional on a `ChunkFetcher` being wired.** With `chunks == nil`
there is no `get_chunks` to recover a body, so `search_knowledge` keeps returning full
text — snipping it there would lose it outright. `wire.go` passes `svcs.PG`
(`search/postgres.Searcher`, whose `FetchChunks` already exists to hydrate vector hits).

`source_url` stays in the summary, so `agent.collectURLs` and the buttons guarantee do not
depend on the full text being present. `TestSearchKnowledgeSummaryMode` pins that.

`internal/mcpserver`'s copy of `search_knowledge` still returns full text: `cmd/ssmcp`
reaches ssapi over HTTP and has no DB handle, so `get_chunks` there needs an ssapi
endpoint first.

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
