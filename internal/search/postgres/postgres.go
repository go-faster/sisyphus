// Package postgres implements full-text search backed by PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

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
	if err != nil {
		return errors.Wrap(err, "exec migrations")
	}
	return nil
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

// FetchChunks loads source-of-truth chunk fields by chunk ID. It is used to
// hydrate vector search hits because Qdrant stores IDs and metadata, not text.
func (s *Searcher) FetchChunks(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]index.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "$" + strconv.Itoa(i+1)
		args[i] = id
	}
	query := `
		SELECT id, text, token_count
		FROM chunks
		WHERE id IN (` + strings.Join(placeholders, ",") + `)
	`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, "query chunks by id")
	}
	defer func() {
		_ = rows.Close()
	}()

	out := make(map[uuid.UUID]index.Chunk, len(ids))
	for rows.Next() {
		var chunk index.Chunk
		if err := rows.Scan(&chunk.ID, &chunk.Text, &chunk.TokenCount); err != nil {
			return nil, errors.Wrap(err, "scan chunk")
		}
		out[chunk.ID] = chunk
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "rows error")
	}
	return out, nil
}

// buildQuery constructs the SQL query and arguments for a full-text search.
// This is extracted as a pure function for easier testing.
func buildQuery(q index.Query) (query string, args []any) {
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}

	args = []any{q.Text, limit}
	var queryStr strings.Builder
	queryStr.WriteString(`
		SELECT id, document_id, chunk_type, coalesce(title,''), text, metadata,
		       ts_rank(search_vector, plainto_tsquery('simple', $1)) AS rank
		FROM chunks
		WHERE search_vector @@ plainto_tsquery('simple', $1)
	`)

	// Combine q.Service (back-compat) and q.Filters into one set of metadata filters.
	filters := make(map[string]string, len(q.Filters)+1)
	maps.Copy(filters, q.Filters)
	if q.Service != "" {
		filters["service"] = q.Service
	}

	if len(filters) > 0 {
		keys := make([]string, 0, len(filters))
		for k := range filters {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		nextArgIdx := 3 // $1 and $2 are text and limit
		for _, k := range keys {
			queryStr.WriteString(fmt.Sprintf(
				" AND metadata @> jsonb_build_object($%d::text, $%d::text)",
				nextArgIdx, nextArgIdx+1,
			))
			args = append(args, k, filters[k])
			nextArgIdx += 2
		}
	}

	queryStr.WriteString(` ORDER BY rank DESC LIMIT $2`)

	query = queryStr.String()
	return query, args
}
