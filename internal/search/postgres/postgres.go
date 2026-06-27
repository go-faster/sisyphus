// Package postgres implements full-text search backed by PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/scpbot/internal/index"
)

//go:embed migrations.sql
var migrationsSQL string

// Searcher implements index.Searcher using Postgres full-text search.
type Searcher struct {
	db *sql.DB
}

// New creates a new Postgres searcher.
func New(db *sql.DB) *Searcher {
	return &Searcher{db: db}
}

// Migrate applies schema migrations to add FTS columns and indexes.
func (s *Searcher) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, migrationsSQL)
	return errors.Wrap(err, "exec migrations")
}

// Search executes a full-text search query against the chunks table.
func (s *Searcher) Search(ctx context.Context, q index.Query) ([]index.Result, error) {
	query, args := buildQuery(q)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, "query chunks")
	}
	defer func() {
		_ = rows.Close()
	}()

	var results []index.Result
	for rows.Next() {
		var (
			id         uuid.UUID
			documentID uuid.UUID
			chunkType  string
			title      string
			text       string
			metadataB  []byte
			rank       float64
		)
		err := rows.Scan(&id, &documentID, &chunkType, &title, &text, &metadataB, &rank)
		if err != nil {
			return nil, errors.Wrap(err, "scan row")
		}

		var metadata map[string]any
		if len(metadataB) > 0 {
			err = json.Unmarshal(metadataB, &metadata)
			if err != nil {
				return nil, errors.Wrap(err, "unmarshal metadata")
			}
		}

		results = append(results, index.Result{
			Chunk: index.Chunk{
				ID:         id,
				DocumentID: documentID,
				Type:       index.ChunkType(chunkType),
				Title:      title,
				Text:       text,
				Metadata:   metadata,
			},
			Score:  rank,
			Vector: false,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "rows error")
	}

	return results, nil
}

// buildQuery constructs the SQL query and arguments for a full-text search.
// This is extracted as a pure function for easier testing.
func buildQuery(q index.Query) (query string, args []any) {
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}

	args = []any{q.Text, limit}
	queryStr := `
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, plainto_tsquery('simple', $1)) AS rank
		FROM chunks
		WHERE search_vector @@ plainto_tsquery('simple', $1)
	`

	if q.Service != "" {
		queryStr += ` AND metadata->>'service' = $3`
		args = append(args, q.Service)
	}

	queryStr += ` ORDER BY rank DESC LIMIT $2`

	query = queryStr
	return
}
