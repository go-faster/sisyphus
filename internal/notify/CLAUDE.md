# internal/notify

`event.Router` → projector → dispatcher/broadcaster → outbox → sink. The contract is in
`notify.go`'s package doc; delivery rides `internal/queue`.

This file holds the rules that span packages — buttons reach across five, the alert loop
across four — so they are here rather than split across the packages that implement them.
Notify is where they converge and where the failure modes live.

## Two addressing modes

`Dispatcher` matches an event's recipient to subscribed users (GitLab MR assignment,
comments, approvals, merges; Jira issue assignment and comments). `Broadcaster` writes one
row per chat registered with `/alerts on`, for events addressed to nobody in particular
(`investigation`).

Notify **fetches nothing** — the GitLab/Jira source adapters emit the events
(`internal/ingest`).

**Who a GitLab/Jira actor is on Telegram comes from `notify.identities` in config and
nowhere else.** ssapi reconciles it on startup (`store.SyncIdentities`); no bot command can
claim an identity, because nothing a user types proves the account is theirs. Subscriptions
stay self-service.

## Staleness: source events state current state, not a change

A source event carries **current** membership, so `notify.Staleness`
(`notify.max_assignment_age_seconds`, 24h default) drops assignments the source dates older
than the cutoff. Without it, any edit to a long-assigned issue re-announces the assignment,
and a fresh outbox announces every one at once.

It is deliberately permissive: an **unknown timestamp still notifies**. Over-notifying
costs one message the dedup key collapses anyway; under-notifying loses a real assignment
silently.

The same cutoff bounds four more events whose dedup keys need no timestamp but whose
payloads restate them forever:

- **comments** — see below.
- **`mr_merged`** — an MR merges once, but the event says "merged" on every poll after. Only `merged_at` separates a merge that just landed from one that landed months ago.
- **`mr_approved`** — the same shape one step earlier: an approval stands in every payload from the moment it is given, so only its own timestamp tells a fresh one from a weeks-old one riding along on an unrelated push.
- **`mr_thread_resolved`** — likewise standing state: a thread stays resolved in every payload after it closes, so `resolved_at` is what separates one just settled from one settled last month.

## Buttons

Answers and notifications carry actionable links as Telegram inline URL buttons.

`index.Link{Text,URL}` (`Valid()` requires an absolute http(s) URL and a non-empty label)
and `index.Answer{Text,Links}` are the shared types. Buttons cross HTTP as
`ContextResponse.buttons`: `internal/api` populates, `internal/apiclient` re-validates,
`internal/bot` renders.

**A button URL must come from a vetted source, never from content.** Both `/context` paths
constrain `submit_answer`'s buttons to the retrieved sources' `source_url`
(`filterButtons`), so the model cannot surface a hallucinated or off-context link. The
agentic path also allows URLs the loop *discovers* mid-conversation — and
`agent.collectURLs` takes those **only** from structured `"source_url"`/`"url"` JSON keys
in a tool result, never by regexing its text. Tool results carry untrusted ingested content
(a chunk body, a fetched page), and a whole-text scan would promote any link merely
*mentioned* there into a clickable button. Keep this if `collectURLs` or its call site
changes.

`/investigate` is deliberately looser: `Report.Links` may be any http(s) URL the agent got
from a tool result (dashboards, tickets). `Report.normalize` drops invalid and duplicate
links and caps at `maxReportLinks`.

**A button must never cost the message.** Telegram sends text and keyboard as one
`sendMessage`, so a URL it rejects takes the whole notification with it — that is how a
batch of firing-alert notifications was lost to one Alertmanager button pointing at a
container-internal hostname. Two defences, both required:

- `index.PublicURL` drops a button whose host has no dot (a container id, `localhost`, an intranet short name — it could not resolve on the recipient's phone anyway), at every point one becomes a Telegram button: `bot.linksMarkup`, `notify.Button.Valid`.
- `bot.SendTo` degrades: a send that fails with a keyboard is retried without it, same `random_id`, warning with the URLs.

The filter is deliberately **not** on `index.Link.Valid` — a Link is also a citation, and
an intranet URL is a fine citation for a reader on the intranet.

Notifications ride their own rail: `notify.Button` → outbox payload →
`PendingNotification.buttons` → `bot.SendTo`, text and keyboard in one `sendMessage` (two
messages would double the notification, and only the first carries the deduplicating
`random_id`). The **projector** enforces the guarantee here: `alert` promotes only
*annotation* values (a rule author wrote those; an alert's labels and description carry
whatever the alerting target reported), GitLab/Jira promote only the subject's own URL, and
`investigation` reuses the already-normalized `Report.Links`. ssbot cannot build buttons
itself — it has no DB and no event, only the rendered row it drained over HTTP.

## Comments and mentions

Assignment says work arrived; comments say the conversation about it moved. Both ride the
same events (`mr.updated`, `issue.updated`) — the payload carries the object's newest
comments alongside its current members — and `notify.CommentRule` (`comments.go`) holds the
fan-out rule for both sources, because that rule is where the failure modes are:

- **Volume is asymmetric, so the two fan out differently.** Assignment is one message per object ever; comments are unbounded. A **mention** notifies per comment — being named is explicit and rare, and collapsing two would lose a question addressed to you. A **comment** notifies once per (**thread**, recipient) per batch, for the newest one in that thread they did not write, so a thread that gets twenty replies between polls is one message.
- **The coalescing key is the thread, not the object.** Two remarks on two lines of a diff are two pieces of news; collapsing them onto the newest drops a review comment outright, which is what happened when a reviewer left two twenty seconds apart inside one poll window. GitLab supplies the discussion id (`Comment.ThreadID`, from the discussions endpoint); Jira has no threads and leaves it empty, which groups all its comments and keeps the one-per-object behavior there.
- **The dedup id is the comment id**, never a timestamp: an edit keeps the id, so editing a comment does not re-notify. Keying the coalesced event by the *newest* comment's id is what keeps the next poll's newest comment a fresh notification instead of a permanently suppressed one.
- **Mentions are parsed by the source adapter, not the projector** — only it knows GitLab writes `@username` and Jira writes `[~accountid:…]`/`[~username]`, and the extracted keys land in the same id space recipients are matched on.
- **`Staleness` is the backfill guard**: the poll is incremental on `updated_after` and the event states current comments, so without a cutoff the first run after this shipped would announce every comment in the fetched window. Comments do **not** inherit the assignment's staleness result, though — a comment on an MR assigned to you months ago is still news.
- A comment's button opens the comment (`#note_<id>` on GitLab, `?focusedCommentId=` on Jira): a fragment or parameter on the URL the API returned, never a guessed permalink.
- **On GitLab the MR author is a comment watcher**, alongside assignees and reviewers. An MR opened without assigning anyone has no members at all, so without this a colleague's review comment notified nobody — and opening an MR without self-assigning is the common shape for a small change, not an edge case. It costs nothing elsewhere: `CommentRule` skips a watcher's own comments and dedups by recipient key, so an author who is also the assignee is told once. Jira has no equivalent; its reporter is not projected, since the assignee is who the work is addressed to.

Out of scope, deliberately: watchers/participants/subscribers (needs fetching that does not
happen), and GitLab *issues*, which emit no canonical event at all today.

## A resolved thread

`mr_thread_resolved` (`internal/notify/gitlab/resolved.go`) is the other end of
`mr_commented`: that one says a question was asked, this one says it was settled. It is
addressed to the **thread's own participants, then the MR author**, minus the resolver —
not to assignees and reviewers at large, who on a review with a dozen threads would get a
dozen messages about conversations they were not in.

Resolution is standing state, like an approval: a thread stays resolved in every payload
after it closes, so `Staleness` gates it on the resolution's own timestamp
(`resolved_at`, parsed per note — GitLab reports it per note, and the newest one dates the
thread). The dedup id is keyed on the discussion id rather than that timestamp, so a
thread reopened and resolved again is one piece of news.

`Resolution` carries its own `Participants` and `Excerpt` rather than pointing into
`MRPayload.Comments`: those are capped at `maxPayloadComments`, so on a busy MR the
thread's comments may not be in the payload at all.

## An MR's outcome: approved, merged

`mr_approved` and `mr_merged` share one recipient set — `outcomeRecipients` in
`internal/notify/gitlab`: **author first, then assignees**, deduped. Reviewers are
deliberately absent. They are already told when their review is requested and when the
conversation moves; telling them a colleague also approved, on every MR they review, is the
noise that gets the whole feature muted.

Both come from the MR's **system notes** (`internal/ingest/gitlab/systemnotes.go`), like
assignment does: GitLab has no approval-events API, and the approvals endpoint carries no
timestamps, which staleness needs. An approval is *standing state* — the newest
approve-or-unapprove note per approver decides, so someone who withdrew an approval is not
an approver, and a re-approval after a withdrawal approves again. The dedup id is keyed on
the approver, not a timestamp, so that re-approval does not re-notify: the same person
saying the same thing twice is one piece of news.

`chunkgitlab.MergeRequest.Approvals` is deliberately **not rendered into the chunked
body** — an approval is an event, not something anyone searches an MR's text for, and
keeping it out is what let this ship without a `ChunkerVersion` bump. `MRPayload.Approvals`
is additive, so `MRPayloadVersion` did not move either.

## Alerts: fire → investigate → announce

`POST /webhooks/alertmanager` (on `ssingest serve`) decodes an Alertmanager group into
`event.TypeAlertFiring` events. With `alertmanager.investigate.enabled`,
`agentstore.Subscriber` submits each as an investigation keyed by `Event.ID`. A worker in
`ssagent` runs it, persists the report, then routes `event.TypeInvestigationCompleted` back
onto the spine, where `Broadcaster` writes one outbox row per **registered chat** for ssbot
to deliver.

**Every hop dedups on an id, and no hop is per-user.** The alert's id pins the
firing/resolved transition, not the delivery, so a `repeat_interval` resend is the same
occurrence; the investigation's id is the job's, not its finish time; the outbox key is per
(chat, event). An alert is addressed to whoever watches the channel, not to a linked
GitLab/Jira identity, which an alert does not have — which is why `Notification.user_id` is
nullable and `peer_type` exists: a broadcast row is addressed by its Telegram peer alone.

**Chats register themselves, from inside the chat.** `/alerts on` in a channel or group
writes a `notify_chats` row (`store.RegisterChat`), and the peer it stores comes from the
update that carried the command. Not a convenience: over MTProto an `InputPeerChannel`
needs an access hash, a private channel has no username to resolve one from later, and a
bare `-100…` id addresses nothing on its own. The update is the only place that hash
exists, so capturing it there is what makes a private channel a valid target at all.
Re-running `/alerts on` re-stores the hash, which is how a rotated bot session heals;
`/alerts off` keeps the row so the hash survives. The bot must be an admin in a channel to
receive the command there at all.
