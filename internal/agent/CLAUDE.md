# internal/agent

The shared LLM tool-calling loop (`coreLoop` in `core.go`), used by **both**:

- `/investigate` (`loop.go`, terminal tool `submit_report` → `Report`)
- the agentic `/context` path (`internal/answer.ContextLoop`)

A change to `coreLoop` therefore lands on both surfaces. Check the other one.

## collectURLs: only structured keys, never free text

The loop collects `DiscoveredURLs` from tool results so the answerer can allow links the
agent found mid-conversation. `collectURLs` extracts URLs **only** from structured
`"source_url"` / `"url"` JSON keys in a tool's result — **never** by regexing the full
result text.

Tool results carry untrusted ingested content: a chunk's body, a fetched page. A naive
whole-text URL scan would let any link merely *mentioned* in that content become a
clickable button, defeating the "buttons are constrained to vetted sources" guarantee that
`filterButtons` exists to provide.

Keep this restriction if `collectURLs` or its call site changes. See
`internal/notify/CLAUDE.md` § Buttons for the whole path.

## Tool results are capped, and URLs are collected before the cap

`coreLoop` truncates every tool result at `defaultMaxToolResultBytes` (`toolresult.go`)
before fencing it into the conversation. A result is appended once and then re-sent on
every later iteration, so one unbounded result is paid for once per remaining iteration.
A 487KB browser snapshot (a Wikipedia article) truncates to 192KB and lands as ~48.6k
prompt tokens in a deployed run; untrimmed it would have been ~120k, in every subsequent
request.

**`collectURLs` must run on the full result, before truncation.** Truncation can cut
mid-JSON, and `collectURLs` only reads structured keys from parseable JSON, so collecting
afterwards silently yields nothing and every vetted `source_url` disappears from the
allowed set. `TestCollectURLsBeforeTruncation` pins this ordering.

The cap is a safety valve, not a quality lever: it is sized from measured results to
leave a full `search_knowledge` response (133KB at limit 30) untouched. Shrinking a chatty
tool's own result is the better fix where one exists — that same tool costs 20KB at
limit 12.

**Size this in bytes, measured from a real result.** An earlier revision used 64KB,
derived by dividing one run's byte count by a different run's token count; it would have
halved a legitimate search response while missing most snapshots. Token counts are not a
substitute for measuring the thing itself.

## Report links

`Report.Links` comes from `submit_report`'s `links` param. Unlike `/context` buttons, these
may be **any** http(s) URL the agent obtained from tool results (dashboards, tickets) —
`/investigate` is deliberately looser. `Report.normalize` drops invalid and duplicate links
and caps at `maxReportLinks`.
