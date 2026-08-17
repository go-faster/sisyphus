# internal/reconcile

`ssingest reconcile`: list every object upstream, delete indexed documents that are no
longer there. Cursor-based ingestion cannot see a deletion — a deleted issue produces no
update, it simply stops existing — so this is the only thing that closes that gap.

It is also the only place in the system that deletes indexed content on the evidence of an
**absence**. Everything else deletes what it can prove is garbage: a vector point no chunk
references, a queue job that settled days ago. An absence is far weaker evidence, because
upstream has several ways to produce one that is not a deletion:

- a token revoked or scoped down
- a project renamed, moved, or made private
- an account that lost access to a project
- a listing that failed halfway and returned 200 with fewer items

Every guard below exists because one of those is indistinguishable from "someone deleted
everything". Do not relax one without knowing which.

## The guards

**A listing error fails its scope, and deletes nothing.** The partial listing is the
dangerous input — not the failure. `Scope.List` must return an error rather than a short
list; both listers refuse a paginator that never shows a short page, for the same reason.

**An empty listing against a non-empty index is always refused**, at any size, before the
fraction check — which would otherwise wave it through whenever the index is small. Nobody
deletes every issue in a project; something broke.

**A diff above `MaxDeleteFraction` (default 20%) is refused, logged, and not applied.**
Real deletions trickle; a broken listing arrives as a cliff. Below `MinIndexedForFraction`
(default 20 documents) the fraction is meaningless — one of three is already a third — so
small scopes reconcile unguarded.

**Refusing is not an error.** It is the guard working, reported per scope so an operator
can see which project and why. Only a listing or delete *failure* is an error.

## Per-project scoping is what keeps a config edit from erasing a corpus

`gitlab_issue`, `gitlab_mr`, `gitlab_release` and `jira` are **global** sources: every
configured project's documents share one source, distinguished only by the `source_id`
prefix. So a reconcile that diffed a whole source against one project's listing would
delete every other project's documents.

`Scope.IDPrefix` is what bounds it, and the consequence is deliberate: **a project not in
config has no scope, so its documents are never diffed and never deleted.** Removing a
project from `gitlab.projects` stops updating its documents; it does not erase them.
`TestUnconfiguredProjectGetsNoScope` and `TestScopesBindTheirOwnProject` pin both halves.

## The listers must mirror the chunkers

`ingest/gitlab.ListSourceIDs` and `ingest/jira.ListSourceIDs` produce exactly the strings
`internal/chunk/{gitlab,jira}` write as `Document.SourceID` —
`<project>/issues/<iid>`, `<project>/merge_requests/<iid>`, `<project>/releases/<tag>`,
and the bare Jira key. If either side changes that shape, a reconcile finds **every**
document missing and only the fraction guard stands between that and deleting the source.
Tests on both sides name the coupling.

They are deliberately not the `Fetch*` methods, which pull discussions, links and
changelogs per object: right for the handful of items an incremental run sees, ruinous
across a whole project. The listers request identifiers only (`fields=key` on Jira) and
pin `state=all` on GitLab, because a listing filtered to open issues would delete every
closed one.

## Delete order

Capture chunk IDs → delete rows in one transaction → drop vector points, non-fatally. Same
order as the ingest-side prune, for the same reason: a failure after the commit leaks
points, which `ssingest gc` reclaims, while the reverse leaves rows pointing at points that
no longer exist, which nothing can repair.
