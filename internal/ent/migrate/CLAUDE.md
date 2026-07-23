# internal/ent/migrate

`internal/ent/schema` is the single source of truth for the DB schema. Versioned SQL files
live in `migrations/` and are applied by the hand-written `Runner` in `runner.go`, tracked
via a `schema_migrations` table.

`Runner.Run` holds a Postgres advisory lock (`advisoryLockID`) for its whole duration, so
concurrent callers serialize instead of racing a `schema_migrations` primary-key conflict —
the second caller blocks, then finds nothing pending once the first commits.

## Migrations never run in the serving path

Only the one-shot `ssapi migrate` subcommand (`wire.Migrate`). In Helm that's the
`migrateJob` pre-install/pre-upgrade hook, which Helm blocks on before creating or updating
any Deployment; in compose it's the `ssmigrate` one-shot service that `ssapi`/`ssingest`
depend on via `condition: service_completed_successfully`.

No serving process — `ssapi`, `ssingest`, `ssbot`, `ssmcp` — migrates itself or holds a
`RunMigrations`-style flag. Do not add one.

`ssapi`'s readiness check (`internal/wire.healthChecker`) fails `/ready` while
`Runner.Pending` reports anything unapplied, so a replica never serves against a schema it
doesn't know. That's also how `ssingest`'s `wait-for-ssapi` init container transitively
waits for the schema.

## Generating a migration

```
make migrate-diff NAME=add_foo_column
```

Needs a local Docker daemon and nothing else running. Uses ent's
`sql/versioned-migration` (`gen/`): spins up a scratch Postgres via `testcontainers-go`,
replays the existing files, diffs against the ent schema, writes the new file, updates
`migrations/atlas.sum`, tears down.

## Hand-written migrations, and the atlas.sum dance

Some DDL can't be expressed in the ent schema — `00002_fts.sql`'s
`GENERATED ALWAYS AS (...) STORED` tsvector column, because ent only supports plain
`DEFAULT`/`DefaultExpr`, which don't recompute on `UPDATE`. Data migrations can't be
produced by a schema diff at all (e.g. `20260723061500_backfill_notification_queue.sql`,
which gives already-queued notifications a delivery job so the outbox move doesn't strand
them). Both are written directly in `migrations/`.

A hand-written file invalidates `atlas.sum`, and every later `make migrate-diff` then
refuses to start on the checksum mismatch:

1. `make migrate-hash` — rehash the directory (no Docker needed)
2. `make migrate-diff` — confirm it replays cleanly and produces **no new file**

No new file means the ent schema and the migrations agree. **Never hand-edit `atlas.sum`.**

## Two more rules

**Forward migration only.** The runner execs the entire file as one blob with no
down/rollback support — stray SQL after a `-- +goose Down`-style comment will actually
execute.

**Squash, don't layer, before it ships.** A migration not yet merged or deployed should be
squashed (delete the file(s), restore the prior `atlas.sum`, rerun `make migrate-diff`)
rather than patched with a follow-up rename migration — otherwise every future
environment's history carries a permanently dangling table.
