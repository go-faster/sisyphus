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

## files

Per-set sources keyed `context_files:<name>`. No cursor — it re-walks, relying on the
pipeline's body-hash skip. Everything it produces is chunked as Markdown
(`indexjob.KindMarkdown`).

- A file that is not valid UTF-8 is **silently skipped**, which is what made every office document in a configured root invisible before the converter existed.
- With `Options.Converter` set (`internal/convert/anydoc`), a file whose extension the converter supports is converted to Markdown instead, and carries `lang: markdown` plus `converted_from: <ext>`. Include/exclude decide first, so a converter can never widen a source's configured file set.
- The converter runs anydoc as a **subprocess, one per document**: a deadline, an output cap and a separate address space turn a hang, a runaway allocation or a crash into one lost document, and keep every binary `CGO_ENABLED=0`. Gating is on the **extension**, not anydoc's own content sniffing, because the walk decides before it reads the file — so a mislabeled document is skipped as before, not converted. No OCR: a scanned PDF still fails `unsupported`.
- A conversion failure is an `*anydoc.Error` (`Code` labels it), and is **counted and logged, never fatal**: an encrypted spreadsheet must not hide every file walked after it. The converse — a converter that cannot run at all — is rejected before the walk starts, because reporting it per file would skip a whole corpus as a stream of warnings nobody reads.
- Because failures are non-fatal, telemetry is the only place they surface: `sisyphus.convert.documents{format,status}` and `sisyphus.convert.duration{format,status}`, plus an `anydoc.Convert` span carrying the child's pid and exit code. `status` is `ok` or the `Code`, so alert on the non-`ok` rate per format — a run that converts nothing still exits 0.
- A **crash** of ssingest (SIGKILL, not SIGTERM) orphans the in-flight child, which the kernel reparents and which runs to completion. No zombie — `cmd.Run` always reaps — and at most one, since the walk converts sequentially. `Pdeathsig` would close it, at the cost of a Linux-only build tag and [golang/go#27505](https://github.com/golang/go/issues/27505); milliseconds-long children were not judged worth that.
- `BodyHash` is the hash of the **converted** Markdown, so an anydoc upgrade re-embeds every converted document. That is why the version is pinned in `deploy/Dockerfile`. It also means conversion runs on every walk, ahead of the skip: at single-digit ms against a per-document embedding call, that is noise.
- **`.csv` is deliberately not converted**, though anydoc would. It is the one supported format that is already valid UTF-8 text, so it is the only one where enabling the converter would change how documents *already in the index* are indexed, re-embedding every one. Converting only the formats that were previously invisible costs nothing that was working before.

## gitlab

Per-resource-type sources: `gitlab_issue`, `gitlab_mr`, `gitlab_release`. Pagination loop
with cursor `{updated_after}` (RFC3339); issues and MRs sorted by `updated_at` asc,
releases filtered client-side. Cursor advances to max `updated_at` (or `released_at`).

- Issues/MRs carry assignees; MRs also carry reviewers and merge metadata (`merged_at`/`by`, `merge_commit_sha`, source/target branch, draft). The MR's `Author` and `MergedBy` are `chunkgitlab.User`, not strings: a notification addresses the author and attributes the merge to the merger, and collapsing either to "username or else display name" makes a display name silently act as a match key — you would be told you merged your own MR. `User.Label()` is the prose form. That change is not backward-compatible on the wire, which is what `indexjob.Version` is for: a job enqueued before it is rejected by version rather than silently decoded into the new shape.
- Cross-references (`closes`/`relates_to`, via the issue-links / MR closes-issues endpoints) are fetched **best-effort and non-fatal** — they can be edition- or permission-gated.
- Comments come from the **discussions** endpoint, not flat notes, so threads and resolved state survive. Trivial notes are filtered per-note; empty threads dropped. Each note keeps its **id and author identity** (`AuthorUser`), which the comment notifications key their dedup id on and address their actor with — the chunker's `Author` string alone cannot do either. `comments.go` reduces the threads to the newest `maxPayloadComments` for the event payload and extracts `@username` mentions; mention syntax is parsed here, not in the notify projector, because only this package knows it. Each payload comment also keeps its **discussion id** (`ThreadID`) — that is what the destination coalesces on, so two remarks on two lines of a diff are two notifications while twenty replies in one thread stay one.
- A thread's **resolution** rides the payload as `Resolution` (`resolutions`): who closed it, when, its participants and its opening comment. `resolved_by`/`resolved_at` come off the notes, where GitLab puts them — per note, so the newest resolution dates the thread and a reopened-and-resolved thread is dated by the second. A note with a null `resolved_by` still sets the timestamp, for the same reason a null-authored assignment note does: an unknown actor renders as "Someone", while a stale timestamp lets `notify.Staleness` drop a fresh resolution. `Resolution` copies its participants and excerpt rather than referencing `Comments`, which the `maxPayloadComments` cap can have dropped. `Thread.ResolvedBy`/`ResolvedAt` are **not** rendered into the chunked body — resolution is an event, not something anyone searches an MR's text for — so adding them needed no `ChunkerVersion` bump, and both payload fields are additive, so `MRPayloadVersion` did not move.
- System notes are filtered out of those threads but still **read for the actors they name** (`systemnotes.go`): who last assigned, who last requested review, who touched the MR last, and who has **approved**. Approvals are a list, not a last-one-wins actor — two approvers are two pieces of news — and the newest approve-or-unapprove note per approver decides, since an approval is standing state someone can withdraw. The approvals *endpoint* is not used: it carries no timestamps, and staleness needs one. An approver GitLab named only by display name is dropped, because the username is what a notification matches a recipient on. GitLab has no assignment-events API and the MR object carries only its author and current members, so the notes are the only record — and the discussions response is already being fetched, so it costs no extra request. The MR **author is not the actor**: they are fixed for the MR's life, so reporting them as the cause of every update names the wrong person. Unknown actor stays zero and renders as "Someone". A note with a **null author still sets its field**, to a zero user with a real timestamp, and logs the drop — skipping it left the *previous* note holding the field, so a reassignment by an unnamed actor reported the previous assigner at the previous assignment's time. The stale timestamp is the worse half: `notify.Staleness` can read it as past the cutoff and drop a fresh assignment, where an unknown one still notifies. **Approvals are the exception** and still skip a null author — there the note's own author *is* the approver, so there is no separate "who" to degrade.
- Every event this package emits is stamped with its payload's schema version (`MRPayloadVersion`, `IssuePayloadVersion`, `AlertPayloadVersion`), and every reader states the version it understands. Bump one when a field changes type or meaning; additive fields need no bump.
- The MR event payload carries `State`/`MergedAt`/`MergedBy` alongside the member sets, which is what `mr_merged` notifications are projected from. Like everything else in the payload it is current state — `merged` is true on every poll from the merge onwards — so the destination gates on `MergedAt`, not on seeing the state change. `Approvals` is the same shape for `mr_approved`: each carries its own timestamp, because it too stands in every payload from the moment it is given. Both fields were additive, so `MRPayloadVersion` did not move.
- Deliberately out of scope: code diffs, wiki, CI/pipeline status, merge-commit ingestion.

## jira

Single source `jira`; incremental via cursor `{last_updated, start_at}`. `--since`
overrides `last_updated`.

- Comments arrive on the issue's `comment` field, and keep their **id and author identity** for the same reason GitLab's notes do. `comments.go` reduces them to the newest `maxPayloadComments` for the event payload and extracts `[~accountid:…]`/`[~username]` mentions — into the same id space `identity()` reports, so a mention resolves against `notify.identities` exactly as an assignee does.
- The search runs with `expand=changelog` (`changelog.go`), because **no issue field says who performed an update**: `reporter` is who filed it, which is a different person as soon as anyone else touches the issue. The newest history entry gives the event's actor, and the newest one touching `assignee` gives the assigner an assignment notification names. **A field set on the create screen produces no changelog entry at all**, so an issue created already assigned has no assignment history to find — and an unknown `AssignedAt` is exactly what `notify.Staleness` waves through, so any later unrelated edit (a sprint rollover is enough) announces a weeks-old assignment as news. `assignedAtCreation` closes that: no assignee entry in a **complete** changelog means the assignee has been there since the issue was filed, so `Created` dates the assignment. Completeness is why `jiraChangelog` models `startAt`/`maxResults`/`total` — on a truncated changelog the missing entry may simply be unsent, so `AssignedAt` stays unknown. The assigner is *not* filled in this way: nothing records who filled the field, and the reporter is who filed the issue. Both actors are found by timestamp, not position, and both stay zero (rendered "Someone") when the changelog names nobody — misattributing an action to a colleague is worse than naming none. **Who and when are answered separately, though**: an entry with a null author (a workflow post-function, a deleted account) still yields its timestamp, and `lastEntry` logs the drop. Discarding the entry outright cost `AssignedAt` too, and an assignment that reaches the projector with an unknown time passes `notify.Staleness` however old it is — so a nameless assigner also disabled the guard that keeps history from being announced as news.

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
