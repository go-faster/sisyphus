# internal/config

The structs carry their own doc comments — read those for what a field means. This file
holds only the traps, which the comments can't show you.

Each service's settings live in a per-service YAML section: `api.*` (ssapi), `mcp.*`
(ssmcp), `telegram.*` (ssbot), `agent.*` (ssagent), `ingest.*` (`ssingest serve`).

## Deprecated flat keys are a hard error when doubled

The old top-level `http_addr`, `mcp_addr` and `mcp_auth_token` still parse. Using one logs
a warning (`Config.Warnings`, surfaced via `Config.LogWarnings`); setting **both** the old
and the new field for the same value is a hard error at `config.Load()` time, not a
precedence rule. See `resolveDeprecatedAddr` / `resolveDeprecatedSecret`.

## `ingest.worker.enabled` is a `*bool` on purpose

A plain `bool` would make every config that omits the section silently disable in-process
indexing. The file struct holds a `*bool` and `resolve()` applies the default (**true**).
Any future "on by default" flag needs the same treatment.

## `ingest.worker.lease_seconds` is also the handler deadline

It is `queue.Delivery.Deadline` — there is deliberately no second timeout knob (see
`internal/queue/CLAUDE.md`). So it must comfortably exceed the slowest single document's
embed-and-upsert, or that document is reclaimed mid-run and retried forever.

## A proxy name is resolved in two places

`proxies.*` names (`git`, `gitlab`, `jira`, `ollama`, `openrouter`, `fetch`) are switched
on twice:

- `internal/config/config.go`'s `fetchProxyURL` — used for config **validation**
- `internal/fetch/fetcher.go`'s `proxyURL` — used to actually **build** the site's `http.Client`

Adding a name to one but not the other either fails validation for a working proxy, or
silently fetches with no proxy at all. Keep both switches in sync.

## The bot allowlist fails closed

`telegram.allowed_chats` / `allowed_user_ids` are both empty by default, and an empty
allowlist means the bot **silently ignores every message** (`internal/bot.Bot.isAllowed`).
Not a misconfiguration the bot reports — it just never answers.

## `context.agentic` needs OpenRouter, and says nothing if it's missing

`agentic: true` only takes effect when OpenRouter is also configured; otherwise `wire.New`
silently falls back to the non-agentic answerer. There is no startup warning for this
today.
