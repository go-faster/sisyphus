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

Keep this restriction if `collectURLs` or its call site changes. See the root
`CLAUDE.md` § "Answers & link buttons" for the whole path.

## Report links

`Report.Links` comes from `submit_report`'s `links` param. Unlike `/context` buttons, these
may be **any** http(s) URL the agent obtained from tool results (dashboards, tickets) —
`/investigate` is deliberately looser. `Report.normalize` drops invalid and duplicate links
and caps at `maxReportLinks`.
