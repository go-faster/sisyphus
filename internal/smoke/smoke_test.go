//go:build integration

// Package smoke contains cross-source ingestion+search smoke tests: it feeds a
// fake document from every supported source through the real chunker+pipeline,
// then looks each one up through the real Postgres searcher using the default
// "curated" source tier — the exact path a bare `/search <ref>` takes.
//
// It is a regression guard for the bug where jira/gitlab chunks carried no
// metadata.source and were therefore silently dropped by source-prefix
// filtering, making direct references (e.g. a Jira key or MR number) unfindable
// even though they were ingested and ranked #1 by raw FTS.
package smoke

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	chunkgitlab "github.com/go-faster/sisyphus/internal/chunk/gitlab"
	chunkjira "github.com/go-faster/sisyphus/internal/chunk/jira"
	chunkmarkdown "github.com/go-faster/sisyphus/internal/chunk/markdown"
	"github.com/go-faster/sisyphus/internal/ent"
	entmigrate "github.com/go-faster/sisyphus/internal/ent/migrate"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/pipeline"
	"github.com/go-faster/sisyphus/internal/search/postgres"
)

// curatedPrefixes mirrors internal/api's default "curated" source tier — the
// prefix set a bare /search (no filters, no tier) resolves to. It intentionally
// lists jira and gitlab_* next to the git prefixes; if a source's chunks omit
// metadata.source, this filter silently excludes them, which is the regression
// under test. Keep in sync with internal/api/source_policy.go.
var curatedPrefixes = []string{
	index.SourceContextFilesPrefix,
	index.SourceGitDocsPrefix,
	index.SourceGitManifestPrefix,
	string(index.SourceJira),
	string(index.SourceGitLabIssue),
	string(index.SourceGitLabMR),
	string(index.SourceGitLabRelease),
}

// routeChunker dispatches to the real per-source chunker the same way ingestion
// does, so the smoke test exercises production chunking, not a stand-in.
type routeChunker struct {
	jira     index.Chunker
	gitlab   index.Chunker
	markdown index.Chunker
}

func (r routeChunker) Chunk(ctx context.Context, doc index.Document) ([]index.Chunk, error) {
	switch {
	case doc.Source == index.SourceJira:
		return r.jira.Chunk(ctx, doc)
	case strings.HasPrefix(string(doc.Source), "gitlab_"):
		return r.gitlab.Chunk(ctx, doc)
	default:
		return r.markdown.Chunk(ctx, doc)
	}
}

func TestSearchSmoke_AllSourcesFindableUnderCuratedTier(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := t.Context()
	container, err := tcpostgres.Run(ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase("sisyphus"),
		tcpostgres.WithUsername("sisyphus"),
		tcpostgres.WithPassword("sisyphus"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	// Real migrations, including the hand-written FTS column/index.
	require.NoError(t, entmigrate.NewRunner(db).Run(ctx))

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	searcher := postgres.New(db, client)

	// Lexical-only pipeline: nil vectors + nil embedder skip Qdrant/Ollama, so
	// the smoke test is hermetic (only Postgres). Source filtering is a Postgres
	// concern, so this is enough to exercise the regression.
	chunker := routeChunker{
		jira:     chunkjira.New(),
		gitlab:   chunkgitlab.New(),
		markdown: chunkmarkdown.New(chunkmarkdown.ChunkerOptions{}),
	}
	pipe, err := pipeline.New(client, chunker, nil, nil, pipeline.PipelineOptions{})
	require.NoError(t, err)

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Each fixture carries a globally-unique nonsense token so a match can only
	// come from its own document — no accidental cross-source hits.
	cases := []struct {
		name    string
		doc     index.Document
		query   string // distinctive token expected only in this doc
		wantSrc string // metadata.source the chunk must carry
		wantURL string // metadata.source_url the chunk must carry
	}{
		{
			name: "jira_issue",
			doc: chunkjira.DocumentFromIssue(chunkjira.Issue{
				Key:         "BILL-4242",
				Title:       "Zorptangle metrics query needs fixing",
				Description: "The zorptangle dashboard shows wrong node metrics.",
				Status:      "In Progress",
				WebURL:      "https://jira.example.com/browse/BILL-4242",
				Created:     now,
				Updated:     now,
			}),
			query:   "BILL-4242 zorptangle",
			wantSrc: string(index.SourceJira),
			wantURL: "https://jira.example.com/browse/BILL-4242",
		},
		{
			name: "gitlab_mr",
			doc: chunkgitlab.DocumentFromMergeRequest("group/proj", chunkgitlab.MergeRequest{
				IID:         7777,
				Title:       "feat: add quibbleframe node field support",
				Description: "Adds quibbleframe support to the node metrics API.",
				State:       "opened",
				WebURL:      "https://gitlab.example.com/group/proj/-/merge_requests/7777",
				Created:     now,
				Updated:     now,
			}),
			query:   "quibbleframe",
			wantSrc: string(index.SourceGitLabMR),
			wantURL: "https://gitlab.example.com/group/proj/-/merge_requests/7777",
		},
		{
			name: "gitlab_issue",
			doc: chunkgitlab.DocumentFromIssue("group/proj", chunkgitlab.Issue{
				IID:         3131,
				Title:       "flibbertwidget crashes on startup",
				Description: "The flibbertwidget component panics during init.",
				State:       "opened",
				WebURL:      "https://gitlab.example.com/group/proj/-/issues/3131",
				Created:     now,
				Updated:     now,
			}),
			query:   "flibbertwidget",
			wantSrc: string(index.SourceGitLabIssue),
			wantURL: "https://gitlab.example.com/group/proj/-/issues/3131",
		},
		{
			name: "gitlab_release",
			doc: chunkgitlab.DocumentFromRelease("group/proj", chunkgitlab.Release{
				TagName:     "v9.9.9",
				Name:        "Wobblesprocket release",
				Description: "Ships the wobblesprocket rollout driver.",
				ReleasedAt:  now,
				WebURL:      "https://gitlab.example.com/group/proj/-/releases/v9.9.9",
			}),
			query:   "wobblesprocket",
			wantSrc: string(index.SourceGitLabRelease),
			wantURL: "https://gitlab.example.com/group/proj/-/releases/v9.9.9",
		},
		{
			name: "git_docs",
			doc: markdownDoc(index.SourceGitDocs("group/proj"),
				"https://gitlab.example.com/group/proj/-/blob/main/runbook.md",
				"Gr* Deploy Runbook", "# Grumbletron deploy\n\nRun the grumbletron rollout before deploy.\n"),
			query:   "grumbletron",
			wantSrc: string(index.SourceGitDocs("group/proj")),
			wantURL: "https://gitlab.example.com/group/proj/-/blob/main/runbook.md",
		},
	}

	for _, tc := range cases {
		require.NoErrorf(t, pipe.Index(ctx, tc.doc), "index %s", tc.name)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := searcher.Search(ctx, index.Query{
				Text:           tc.query,
				Limit:          20,
				SourcePrefixes: curatedPrefixes,
			})
			require.NoError(t, err)

			var found *index.Result
			for i := range results {
				if results[i].Chunk.DocumentID == tc.doc.ID {
					found = &results[i]
					break
				}
			}
			require.NotNilf(t, found,
				"%s document %q not returned under the curated tier for query %q (got %d results) — "+
					"a direct reference to this source is unfindable via default search",
				tc.name, tc.doc.SourceID, tc.query, len(results))
			require.Equalf(t, tc.wantSrc, found.Chunk.Metadata["source"],
				"%s chunk must carry metadata.source", tc.name)
			require.Equalf(t, tc.wantURL, found.Chunk.Metadata["source_url"],
				"%s chunk must carry metadata.source_url for the Source link button", tc.name)
		})
	}
}

func markdownDoc(source index.Source, url, title, body string) index.Document {
	return index.Document{
		ID:       index.NewID(),
		Source:   source,
		SourceID: string(source) + ":runbook.md",
		URL:      url,
		Title:    title,
		Body:     body,
		BodyHash: index.Hash(body),
		Metadata: map[string]any{
			"authority": string(index.AuthorityHigh),
		},
	}
}
