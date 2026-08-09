# internal/config

The structs carry their own doc comments — read those for what a field means. This file
holds only the traps, which the comments can't show you.

Each service's settings live in a per-service YAML section: `api.*` (ssapi), `mcp.*`
(ssmcp), `telegram.*` (ssbot), `agent.*` (ssagent), `ingest.*` (`ssingest serve`).

## Deprecated flat keys are a hard error when doubled

The old top-level `http_addr`, `mcp_addr` and `mcp_auth_token` still parse. Using one logs
a warning (`Config.Warnings`, surfaced via `Config.LogWarnings`); setting **both** the old
and the new field for the same value is a hard error at `config.Load()` time, not a
precedence rule. It is `figureout.MovedFrom` on the new field that does this, so the old
key is declared once, next to the field that superseded it; `postResolve` only re-renders
the diagnostic into the warning phrasing `LogWarnings` has always used.

A moved key's section must be a `figureout.Group`, not a nested descriptor: a former path
resolves in the scope that declares the field, and these old keys are top-level.

## An empty `SISYPHUS_*` variable is not a value

`SISYPHUS_<PATH>` binds a field directly (`SISYPHUS_INGEST_WORKER_CONCURRENCY`), layered
over the file. `Load` feeds the env source `setEnvironment()`, which drops variables that
are **present but empty** — `deploy/docker-compose.yml` passes every credential as
`${SISYPHUS_X:-}`, so each one exists and is empty in every container unless the operator
filled it in, and a bare `env.Current` would let that blank a token `config.yaml` sets
literally. Keep the filter, and do not add a config field that wants `""` set on purpose.

The corollary is in tests: `clearEnv` sweeps *every* `SISYPHUS_*` variable, not a list, or
a developer's exported token binds a field in a test that never mentions one.

## "On by default" flags

`ingest.worker.enabled` and `alertmanager.notify.enabled` default to **true**: a config
that omits the section must not silently disable in-process indexing, or mute every alert.
Presence is the source layer's job (`ApplyDefault(true)`), so these are plain `bool` —
they were `*bool` before the descriptor, and a new flag needs no such hack.

## `maintenance.*` intervals are durations, and `0` means off

The older sections spell periods as `*_seconds` ints; `maintenance.*` uses `time.Duration`
(`24h`, `168h`), because these are hours and days and `604800` is not a reviewable value.
figureout parses durations natively — a new period field should follow this, not the ints.

An interval of `0` disables that job rather than meaning "as fast as possible", and a
`maint` process with every job disabled idles instead of exiting, so turning maintenance
off in config does not produce a crash-looping container.

`maintenance.gc.interval` must exceed `maintenance.gc.grace`, enforced by an invariant:
each sweep *spends* `grace` waiting between its two passes, so an interval below it
schedules the next sweep before the current one can finish.

## `ingest.worker.lease_seconds` is also the handler deadline

It is `queue.Delivery.Deadline` — there is deliberately no second timeout knob (see
`internal/queue/CLAUDE.md`). So it must comfortably exceed the slowest single document's
embed-and-upsert, or that document is reclaimed mid-run and retried forever.

## A proxy name is resolved in two places

`proxies.*` names (`git`, `gitlab`, `jira`, `ollama`, `openrouter`, `fetch`) are switched
on twice:

- `internal/config/validate.go`'s `fetchProxySecret` — used for config **validation**
- `internal/fetch/fetcher.go`'s `proxyURL` — used to actually **build** the site's `http.Client`

Adding a name to one but not the other either fails validation for a working proxy, or
silently fetches with no proxy at all. Keep both switches in sync.

## The bot allowlist fails closed

`telegram.allowed_chats` / `allowed_user_ids` are both empty by default, and an empty
allowlist means the bot **silently ignores every message** (`internal/bot.Bot.isAllowed`).
Not a misconfiguration the bot reports — it just never answers.

## Two timeouts bound a `/context` answer, and the smaller one is ssbot's

`context.timeout_seconds` bounds the answerer; `telegram.answer_timeout_seconds` bounds
how long ssbot waits for it. For a Telegram user the **smaller** one wins, and ssbot's
defaults to 60s — so raising `context.timeout_seconds` alone changes nothing they can see.
It reaches only direct API/MCP callers. Raise both, together.

## `context.agentic` needs OpenRouter, and says nothing if it's missing

`agentic: true` only takes effect when OpenRouter is also configured; otherwise `wire.New`
silently falls back to the non-agentic answerer. There is no startup warning for this
today.
