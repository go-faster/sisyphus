package postgres

import (
	"database/sql"
	"maps"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
)

// openTestDB opens the shared test database and ensures the base schema exists.
// ent owns the tables; migrations.sql layers the FTS column and index on top.
func openTestDB(t *testing.T) (*sql.DB, *ent.Client) {
	t.Helper()
	dsn := os.Getenv("SISYPHUS_TEST_DB")
	if dsn == "" {
		t.Skip("set SISYPHUS_TEST_DB to run DB tests")
	}
	// The pgx driver registers itself via the blank import above; without it
	// sql.Open fails with `unknown driver "pgx"`.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	if err := client.Schema.Create(t.Context()); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	return db, client
}

// TestBuildQuery tests the pure query-building function.
func TestBuildQuery(t *testing.T) {
	tests := []struct {
		name    string
		q       index.Query
		wantSQL string
		wantLen int
	}{
		{
			name: "basic query",
			q: index.Query{
				Text: "foo bar",
			},
			wantSQL: `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery) AS rank
		FROM chunks
		WHERE search_vector @@ replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery
	 ORDER BY rank DESC LIMIT $2`,
			wantLen: 2, // text, limit
		},
		{
			name: "query with service filter",
			q: index.Query{
				Text:    "test",
				Service: "myservice",
			},
			wantSQL: `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery) AS rank
		FROM chunks
		WHERE search_vector @@ replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery
	 AND metadata @> jsonb_build_object($3::text, $4::text) ORDER BY rank DESC LIMIT $2`,
			wantLen: 4, // text, limit, "service" key, "myservice" value
		},
		{
			name: "query with custom limit",
			q: index.Query{
				Text:  "search",
				Limit: 50,
			},
			wantLen: 2,
		},
		{
			name: "query with zero limit defaults to 30",
			q: index.Query{
				Text:  "search",
				Limit: 0,
			},
			wantLen: 2,
		},
		{
			name: "query with negative limit defaults to 30",
			q: index.Query{
				Text:  "search",
				Limit: -1,
			},
			wantLen: 2,
		},
		{
			name: "query with filters",
			q: index.Query{
				Text:    "search",
				Filters: map[string]string{"status": "In Review"},
			},
			wantSQL: `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery) AS rank
		FROM chunks
		WHERE search_vector @@ replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery
	 AND metadata @> jsonb_build_object($3::text, $4::text) ORDER BY rank DESC LIMIT $2`,
			wantLen: 4, // text, limit, "status", "In Review"
		},
		{
			name: "query with service and filters",
			q: index.Query{
				Text:    "search",
				Service: "myservice",
				Filters: map[string]string{"status": "In Review"},
			},
			wantSQL: `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery) AS rank
		FROM chunks
		WHERE search_vector @@ replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery
	 AND metadata @> jsonb_build_object($3::text, $4::text) AND metadata @> jsonb_build_object($5::text, $6::text) ORDER BY rank DESC LIMIT $2`,
			wantLen: 6, // text, limit, "service", "myservice", "status", "In Review"
		},
		{
			name: "query with multiple filters",
			q: index.Query{
				Text: "search",
				Filters: map[string]string{
					"status":   "In Review",
					"jira_key": "BILL-42",
				},
			},
			wantSQL: `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery) AS rank
		FROM chunks
		WHERE search_vector @@ replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery
	 AND metadata @> jsonb_build_object($3::text, $4::text) AND metadata @> jsonb_build_object($5::text, $6::text) ORDER BY rank DESC LIMIT $2`,
			wantLen: 6, // text, limit, "jira_key", "BILL-42", "status", "In Review"
		},
		{
			name: "query with source prefixes",
			q: index.Query{
				Text:           "search",
				SourcePrefixes: []string{index.SourceGitDocsPrefix, string(index.SourceJira)},
			},
			wantSQL: `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery) AS rank
		FROM chunks
		WHERE search_vector @@ replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery
	 AND (metadata->>'source' LIKE $3::text OR metadata->>'source' = $4::text) ORDER BY rank DESC LIMIT $2`,
			wantLen: 4,
		},
		{
			name: "query with filters and source prefixes",
			q: index.Query{
				Text:           "search",
				Filters:        map[string]string{"status": "In Review"},
				SourcePrefixes: []string{index.SourceGitCodePrefix},
			},
			wantSQL: `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery) AS rank
		FROM chunks
		WHERE search_vector @@ replace(plainto_tsquery('simple', $1)::text, ' & ', ' | ')::tsquery
	 AND metadata @> jsonb_build_object($3::text, $4::text) AND (metadata->>'source' LIKE $5::text) ORDER BY rank DESC LIMIT $2`,
			wantLen: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queryStr, args := buildQuery(tt.q)

			// Check argument count
			if len(args) != tt.wantLen {
				t.Errorf("arg count: got %d, want %d", len(args), tt.wantLen)
			}

			// Check that text is in args
			if len(args) > 0 && args[0] != tt.q.Text {
				t.Errorf("text arg: got %v, want %v", args[0], tt.q.Text)
			}

			// Check limit handling
			if len(args) > 1 {
				limit := args[1].(int)
				expectedLimit := tt.q.Limit
				if expectedLimit <= 0 {
					expectedLimit = 30
				}
				if limit != expectedLimit {
					t.Errorf("limit: got %d, want %d", limit, expectedLimit)
				}
			}

			// Build expected filter set from Service (back-compat) and Filters.
			expectedFilters := make(map[string]string, len(tt.q.Filters)+1)
			maps.Copy(expectedFilters, tt.q.Filters)
			if tt.q.Service != "" {
				expectedFilters["service"] = tt.q.Service
			}

			if len(expectedFilters) == 0 {
				if contains(queryStr, "jsonb_build_object") {
					t.Errorf("jsonb_build_object should not be in SQL when no filters")
				}
			} else {
				if !contains(queryStr, "jsonb_build_object") {
					t.Errorf("expected jsonb_build_object in SQL for filters")
				}
				// Verify each expected filter key and value appear as consecutive args
				// starting from index 2 (after text at 0, limit at 1).
				for k, v := range expectedFilters {
					found := false
					for i := 2; i < len(args)-1; i++ {
						if s, ok := args[i].(string); ok && s == k {
							if s2, ok := args[i+1].(string); ok && s2 == v {
								found = true
								break
							}
						}
					}
					if !found {
						t.Errorf("filter key %q with value %q not found in args", k, v)
					}
				}
			}
		})
	}
}

// TestSearchSkipWithoutDB skips if SISYPHUS_TEST_DB is not set.
func TestSearchSkipWithoutDB(t *testing.T) {
	db, client := openTestDB(t)

	s := New(db, client)
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// A term no chunk contains must match nothing, whatever else the shared
	// database happens to hold.
	results, err := s.Search(t.Context(), index.Query{
		Text:  "zzzznonexistentzzzz",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results for a term no chunk contains, got %d", len(results))
	}
}

// TestMigrate tests that migrations run without error.
func TestMigrateSkipWithoutDB(t *testing.T) {
	db, client := openTestDB(t)

	s := New(db, client)
	// Migrate must be idempotent: it runs on every ssapi start.
	for range 2 {
		if err := s.Migrate(t.Context()); err != nil {
			t.Errorf("Migrate failed: %v", err)
		}
	}
}

// contains is a helper to check if a string contains a substring.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestSearcher ensures Searcher is a Searcher interface impl.
func TestSearcherInterface(t *testing.T) {
	var _ index.Searcher = (*Searcher)(nil)
}

// BenchmarkBuildQuery benchmarks query building.
func BenchmarkBuildQuery(b *testing.B) {
	q := index.Query{
		Text:    "test query",
		Service: "myservice",
		Limit:   50,
	}
	for i := 0; i < b.N; i++ {
		buildQuery(q)
	}
}
