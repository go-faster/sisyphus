package answer

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"

	"github.com/go-faster/sisyphus/internal/agent"
)

// MultiToolSource merges multiple agent.ToolSource into one.
type MultiToolSource struct {
	sources []agent.ToolSource

	mu    sync.RWMutex
	index map[string]agent.ToolSource
}

func NewMultiToolSource(sources ...agent.ToolSource) *MultiToolSource {
	filtered := make([]agent.ToolSource, 0, len(sources))
	for _, src := range sources {
		if src != nil {
			filtered = append(filtered, src)
		}
	}
	return &MultiToolSource{sources: filtered}
}

func (m *MultiToolSource) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	tools := make([]openai.ChatCompletionToolUnionParam, 0)
	indexByName := make(map[string]agent.ToolSource)
	for _, src := range m.sources {
		srcTools, err := src.Tools(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "list tools")
		}
		for _, tool := range srcTools {
			if tool.OfFunction == nil {
				continue
			}
			name := tool.OfFunction.Function.Name
			if _, ok := indexByName[name]; ok {
				return nil, errors.Errorf("duplicate tool %q", name)
			}
			indexByName[name] = src
			tools = append(tools, tool)
		}
	}
	m.mu.Lock()
	m.index = indexByName
	m.mu.Unlock()
	return tools, nil
}

func (m *MultiToolSource) Call(ctx context.Context, name string, argsJSON json.RawMessage) (string, error) {
	m.mu.RLock()
	index := m.index
	m.mu.RUnlock()
	if index == nil {
		if _, err := m.Tools(ctx); err != nil {
			return "", err
		}
		m.mu.RLock()
		index = m.index
		m.mu.RUnlock()
	}
	src, ok := index[name]
	if !ok {
		return "", errors.Errorf("unknown tool %q", name)
	}
	return src.Call(ctx, name, argsJSON)
}

var _ agent.ToolSource = (*MultiToolSource)(nil)
