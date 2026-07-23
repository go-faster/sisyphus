# Architecture

> **Status: target architecture.** This document describes where we are going.
> It reflects a redesign of today's five role-based binaries
> (`ssapi`/`ssingest`/`ssbot`/`ssagent`/`ssmcp`) into **bounded contexts** wired
> by an **event spine**, deployed as **component shapes** chosen for scalability
> and HA. Sections marked _(today)_ describe the current state; everything else
> is the target. `CLAUDE.md` remains the source of truth for the current
> package layout until the migration lands.

Sisyphus is not one product — it is four that share a repo and datastores:

- **A. Knowledge Graph** — hybrid search/RAG over ingested knowledge (self-contained).
- **B. Event Gateway** — one ingress for events from any source; routes them.
- **C. Notification Gateway** — matches events to users; delivers to channels.
- **D. Agentic SRE / Helper** — async investigation/answer over an LLM tool loop.
- **E. Team Bot** — the Telegram Bot-API surface (duty, `/context`, buttons).

The design goal is that every component is either trivially scalable or an
isolated single-active talker, with correctness resting on **idempotency**, not
on replica count.

## First principle: idempotency, not singletons

All writes are idempotent: documents are keyed `(source, source_id)` and
upserted; chunk embeddings skip on unchanged body hash; notification delivery
dedups on a deterministic key. **A second replica never corrupts data — it only
duplicates work.** So "run exactly one" is always a *cost/politeness* decision
(don't double upstream API load), never a correctness gate. Any data corruption
is a bug to fix at the write, not a reason to forbid concurrency.

## Component shapes

Every deployable is one of four shapes. Pick the shape from the constraint, not
the feature.

| Shape | Scaling | Coordination | Used by |
|---|---|---|---|
| **Read replica** | N replicas | none — reads datastores | KG query API, MCP |
| **Queue worker** | N replicas | Postgres queue + `FOR UPDATE SKIP LOCKED`; idempotent | ingest, embed, notify-deliver, agent |
| **CronJob** | runs to completion | k8s `concurrencyPolicy: Forbid` | ingest sweep, gc/repair, full reconcile |
| **Single-active talker** | 1 active | inherent (single-consumer upstream) | Team Bot (`getUpdates`), userbot session |

Notably there is **no leader-elected poll daemon**: periodic pulls are CronJobs
(`Forbid` gives "one at a time" for free), and the only always-on singletons are
the two Telegram talkers, which are single-active by nature of their upstreams.

## The event spine

Sources are heterogeneous; destinations are shared. A single canonical event
type decouples them.

```
  SOURCES                    EVENT GATEWAY (B)              DESTINATIONS
 ┌────────────┐  webhook   ┌───────────────────┐  route  ┌──────────────────┐
 │ GitLab     │──(HA recv)▶│ normalize → Event │────────▶│ (A) KG ingest    │
 │ Jira       │            │ → fan-out router  │────────▶│ (C) Notification │
 │ Alertmgr   │            │                   │────────▶│ (D) Agent (react)│
 │ userbot    │──(cron)───▶│                   │         └──────────────────┘
 └────────────┘            └───────────────────┘
```

- An **Event** is "something happened" (MR updated, alert fired, message
  posted). It is transient and routable.
- A **Document** is "indexable knowledge" derived from an event by the KG's
  ingest adapter.
- A **Notification** is "tell user X about event E" derived by Notify.
- An **Investigation** is "look into event E" derived by Agent.

One event fans out to whichever destinations subscribe. **Adding "notify + react
to alerts" is a new source + new routes, not a new pipeline.** This also
collapses today's duplicate GitLab/Jira polling (ingest and notify polled the
same APIs on separate cursors): poll once, emit one event, route to both.

### The `Event`/router contract (load-bearing)

The canonical `Event` and the `Router` interface become the system's central
contract, on par with `internal/index` today. `Event` must carry enough for
every destination — raw payload (ingest), actor/recipient (notify), and
severity/context (agent) — **without becoming a god-object**. Getting this shape
right is what keeps the four contexts decoupled; getting it wrong couples
everything through it. This is the highest-risk interface in the redesign and
should be reviewed before implementation fans out.

## Bounded contexts

### A. Knowledge Graph — self-contained RAG

**Concern:** ingest normalized documents; answer search/context queries.
**Owns:** `Document`, `Chunk`, retrieval (Postgres FTS + Qdrant, RRF k=60),
answering (OpenRouter / agentic loop). **Knows nothing about GitLab, Jira, or
Telegram** — source adapters hand it `index.Document`s. This is the cleanest
module boundary and the piece most extractable into its own service/repo.

- **Query side** — read replicas (HTTP + MCP). Stateless → N replicas.
- **Ingest side** — queue workers (chunk → embed → upsert Postgres/Qdrant).
  Idempotent → N replicas.
- **Maintenance** — `gc` (orphan vector points, two-pass with grace) and
  `repair` (drifted `chunks.id != qdrant_point_id`) as **CronJobs**, not manual.

### B. Event Gateway — unified ingress + router

**Concern:** receive events from any source, normalize to canonical `Event`,
fan out to destinations. **Owns:** source adapters (fetch + normalize), webhook
signature validation, the router.

- **Webhook receiver** — read-replica shape (stateless, N replicas behind
  Ingress). Validates and enqueues.
- **Periodic pull** — **CronJob** per source (`Forbid`), replacing the poll
  daemon. Incremental by cursor for cost; an infrequent **full reconcile**
  CronJob catches deletions (the one thing incremental misses).
- **userbot backfill** — a source adapter running the gotd **user session**
  (single-active talker, stateful, own PVC). Distinct from the Team Bot.

**On cursors:** a cursor (`updated_after=T`) is a bookmark so a run fetches only
what changed, not the whole backlog. It is *not* "new only" — sources sort by
`updated_at`, so an edited old item resurfaces. It is purely an efficiency
device over idempotent upserts; deletions require the full-reconcile CronJob.

### C. Notification Gateway

**Concern:** match events to subscribed users; deliver to channels.
**Owns:** subscriptions, identity links (GitLab username / Jira accountId /
Telegram), the outbox, delivery. Consumes `Event`s from the router; emits
`Notification`s to a Postgres outbox; **delivery workers** (queue-worker shape)
push to channels. Delivery is at-least-once + idempotent (deterministic
`random_id` on Telegram). Alerts are just another event type feeding this.

### D. Agentic SRE / Helper

**Concern:** async investigation/answer over an LLM tool-calling loop.
**Owns:** investigation jobs (Postgres queue, dedup by idempotency key, stale
reap on startup), the agent loop + tools (search, fetch, ssh-mcp sandbox).
Queue-worker shape → N replicas. Triggered by `/investigate` (bot/API) **and**
by the router (alert fires → auto-investigate → route result back to Notify).

### E. Team Bot — Telegram Bot-API surface

**Concern:** the bot-token interaction only — `/context`, duty/on-call handling,
rendering notifications + inline buttons. **Single-active talker**
(`getUpdates` is single-consumer). Holds no datastore connection; calls A
(query), C (subscriptions), D (investigate) over HTTP. The notify **drain** lives
here (the authenticated session is already here). **Strictly separate from the
userbot backfill** (context B), which is a source, not a surface.

## Deployment topology

Recommended: a **modular monolith** — one codebase with hard package boundaries
per context — deployed as multiple **shapes** rather than split into separate
services up front. This keeps refactor cost low and preserves the option to
extract the Knowledge Graph (the cleanest boundary) into its own service later.

| Deployable | Shape | Replicas |
|---|---|---|
| kg-query (+ MCP) | read replica | N |
| kg-ingest-worker | queue worker | N |
| event-webhook | read replica | N |
| event-sweep / reconcile | CronJob | — |
| userbot-backfill | single-active | 1 (PVC) |
| notify-deliver | queue worker | N |
| agent-worker | queue worker | N |
| team-bot | single-active | 1 |
| migrate | Job (Helm hook / advisory-locked) | 1 |
| gc / repair | CronJob | — |

**Migration ownership:** schema migration moves out of the serving path into a
one-shot **Job** (or a `pg_advisory_lock`-guarded startup), so the KG query
service can finally run N replicas without racing on `schema_migrations` — the
current blocker to `ssapi` HA _(today)_.

## Migration from today's five binaries

Idempotency makes this incremental and safe to do in slices:

1. **Decouple migration from serving.** Move migrations to a Job/advisory lock;
   `ssapi` serving becomes HA read replicas.
2. **Extract the KG ingest worker.** Introduce the Postgres ingest job queue
   (reuse the `agentstore`/notify-outbox pattern); `ssingest`'s pipeline runs as
   a queue worker. Workers go HA.
3. **Replace the poll daemon with CronJobs.** `ssingest serve`'s polling becomes
   per-source CronJobs (`Forbid`) + a full-reconcile CronJob; keep only the
   webhook receiver as an HA service.
4. **Introduce the event spine.** Define canonical `Event` + `Router`; make
   Notify and Agent *subscribers* instead of directly-poked code. Unify the
   duplicate GitLab/Jira polling into one event source.
5. **Split the two Telegrams.** Team Bot (Bot API) and userbot backfill become
   separate deployables.
6. **Automate maintenance.** `gc`/`repair` become CronJobs.

## Constraints that don't go away

- **Telegram is single-active** — both `getUpdates` (bot) and the user session
  (backfill). Isolate them so they never constrain the rest.
- **Cursor incremental never sees deletes** — the full-reconcile CronJob is not
  optional if deletions must propagate.
- **Per-source cursor writes should be serialized** — but only to avoid wasted
  API load, not for correctness; CronJob `Forbid` handles it without leader
  election.

## Auth & trust boundaries (unchanged)

- KG/API: static bearer, `/health` open, empty token fails startup.
- MCP: optional bearer (empty warns, still serves).
- Team Bot: allowlist, fail-closed.
- Sandbox: egress-denied NetworkPolicy, ingress only from its MCP front-end.
