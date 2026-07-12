// Package indextest provides reusable mock implementations for index interfaces.
package indextest

import (
	"context"

	"github.com/go-faster/sisyphus/internal/index"
)

// MockAnswerer implements index.Answerer and records calls for test assertions.
type MockAnswerer struct {
	AnswerResult index.Answer
	Err          error
	Queries      []index.Query
	ResultsSets  [][]index.Result
}

var _ index.Answerer = (*MockAnswerer)(nil)

func (m *MockAnswerer) Answer(_ context.Context, q index.Query, results []index.Result) (index.Answer, error) {
	m.Queries = append(m.Queries, q)
	m.ResultsSets = append(m.ResultsSets, results)
	return m.AnswerResult, m.Err
}
