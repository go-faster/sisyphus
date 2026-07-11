package answer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/agent"
)

type staticSource struct {
	tool openai.ChatCompletionToolUnionParam
	seen []string
	res  string
	err  error
}

func (s *staticSource) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	return []openai.ChatCompletionToolUnionParam{s.tool}, nil
}

func (s *staticSource) Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error) {
	s.seen = append(s.seen, name)
	if s.err != nil {
		return "", s.err
	}
	return s.res, nil
}

var _ agent.ToolSource = (*staticSource)(nil)

func TestMultiToolSource_Merge(t *testing.T) {
	a := &staticSource{tool: searchKnowledgeTool(), res: "a"}
	b := &staticSource{tool: fetchURLTool(), res: "b"}
	m := NewMultiToolSource(a, b)
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2)
}

func TestMultiToolSource_Dispatch(t *testing.T) {
	a := &staticSource{tool: searchKnowledgeTool(), res: "a"}
	b := &staticSource{tool: fetchURLTool(), res: "b"}
	m := NewMultiToolSource(a, b)
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	got, err := m.Call(context.Background(), "fetch_url", nil)
	require.NoError(t, err)
	require.Equal(t, "b", got)
	require.Equal(t, []string{"fetch_url"}, b.seen)
}

func TestMultiToolSource_NilSource(t *testing.T) {
	a := &staticSource{tool: searchKnowledgeTool(), res: "a"}
	m := NewMultiToolSource(nil, a)
	tools, err := m.Tools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1)
}

func TestMultiToolSource_UnknownTool(t *testing.T) {
	a := &staticSource{tool: searchKnowledgeTool(), res: "a"}
	m := NewMultiToolSource(a)
	_, err := m.Tools(context.Background())
	require.NoError(t, err)
	_, err = m.Call(context.Background(), "missing", nil)
	require.Error(t, err)
}
