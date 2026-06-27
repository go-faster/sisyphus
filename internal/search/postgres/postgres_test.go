package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/go-faster/scpbot/internal/index"
)

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
		       ts_rank(search_vector, plainto_tsquery('simple', $1)) AS rank
		FROM chunks
		WHERE search_vector @@ plainto_tsquery('simple', $1)
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
		       ts_rank(search_vector, plainto_tsquery('simple', $1)) AS rank
		FROM chunks
		WHERE search_vector @@ plainto_tsquery('simple', $1)
	 AND metadata->>'service' = $3 ORDER BY rank DESC LIMIT $2`,
			wantLen: 3, // text, limit, service
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

			// Check service filter presence in SQL
			if tt.q.Service != "" {
				if tt.q.Service != "myservice" {
					t.Fatalf("test setup error: q.Service should be 'myservice' or use different service in test")
				}
				if !contains(queryStr, "metadata->>'service'") {
					t.Errorf("service filter not in SQL")
				}
				if len(args) < 3 || args[2] != tt.q.Service {
					t.Errorf("service arg not found or wrong")
				}
			} else if contains(queryStr, "metadata->>'service'") {
				t.Errorf("service filter should not be in SQL when Service is empty")
			}
		})
	}
}

// TestSearchSkipWithoutDB skips if SCPBOT_TEST_DSN is not set.
func TestSearchSkipWithoutDB(t *testing.T) {
	dsn := os.Getenv("SCPBOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set SCPBOT_TEST_DSN to run DB tests")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Test connection
	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	s := New(db)
	if s == nil {
		t.Fatal("New() returned nil")
	}

	// Test that Search returns empty results on empty DB
	results, err := s.Search(context.Background(), index.Query{
		Text:  "nonexistent",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results on empty db, got %d", len(results))
	}
}

// TestMigrate tests that migrations run without error.
func TestMigrateSkipWithoutDB(t *testing.T) {
	dsn := os.Getenv("SCPBOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set SCPBOT_TEST_DSN to run DB tests")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	s := New(db)
	err = s.Migrate(context.Background())
	if err != nil {
		t.Errorf("Migrate failed: %v", err)
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
