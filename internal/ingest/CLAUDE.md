# internal/ingest

Per-source fetchers. Each produces `index.Document`s and advances a cursor stored in the
`SyncState` row its caller owns (see `cmd/ssingest/CLAUDE.md`). GitLab and Jira use stdlib
`net/http`, not a vendored SDK.

## git

Per-repo sources keyed `git_docs:<repo>` (Markdown), `git_commits:<repo>` (commit
messages), `git_tags:<repo>` (opt-in via `tags: true`). Local checkout, or clone/pull via
`git`.

- Docs and tags have **no cursor** — they re-walk, relying on the pipeline's body-hash skip to avoid re-embedding.
- Commits use cursor `{last_sha, branch}` and walk incrementally from HEAD backwards.
- Annotated tags use the tag message/tagger; lightweight tags fall back to the target commit's subject/author.

## gitlab

Per-resource-type sources: `gitlab_issue`, `gitlab_mr`, `gitlab_release`. Pagination loop
with cursor `{updated_after}` (RFC3339); issues and MRs sorted by `updated_at` asc,
releases filtered client-side. Cursor advances to max `updated_at` (or `released_at`).

- Issues/MRs carry assignees; MRs also carry reviewers and merge metadata (`merged_at`/`by`, `merge_commit_sha`, source/target branch, draft).
- Cross-references (`closes`/`relates_to`, via the issue-links / MR closes-issues endpoints) are fetched **best-effort and non-fatal** — they can be edition- or permission-gated.
- Comments come from the **discussions** endpoint, not flat notes, so threads and resolved state survive. Trivial notes are filtered per-note; empty threads dropped.
- Deliberately out of scope: code diffs, wiki, CI/pipeline status, merge-commit ingestion.

## jira

Single source `jira`; incremental via cursor `{last_updated, start_at}`. `--since`
overrides `last_updated`.

## telegram

Single source `telegram`; cursor `{per_chat}`. gotd user-session backfill →
`telegram_messages` → `support_requests`. `MessageFetcher` is the seam for tests;
`bootstrapPeers` resolves access hashes.

`ssingest telegram [dump.json ...]` additionally ingests Telegram Desktop / GDPR chat
export JSON (one file per chat: top-level `id`/`name`/`type`/`messages`, see `dump.go`'s
`Dump`). This runs **independently of the live session** — passing only dump file args,
with no `app_id`/`app_hash`/`ingest_session` configured, is enough to ingest dumps with no
Telegram API credentials.

Dumps are one-shot exports with no pagination cursor: each run re-walks the given files and
relies on the `telegram_messages`/`support_requests` upserts plus the pipeline body-hash
skip to stay idempotent. Service messages (joins/pins/…) and entries with no extractable
text are skipped.

`ssingest all` takes no dump file args — dump ingestion must go through the `telegram`
subcommand directly.
