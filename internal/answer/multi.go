package answer

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
)

// MultiToolSource merges multiple agent.ToolSource into one. Used only by the
// /context path (see wire.New) to combine the always-in-process knowledge
// tools with the optional MCP gateway/sandbox tools. Its per-source degrade
// (see Tools below) is on top of, not instead of, Engine.Run's own tolerance
// for a toolSource.Tools failure — that one covers /investigate's single MCP
// source too, this one additionally keeps /context's knowledge tools working
// even when only the gateway source is down.
type MultiToolSource struct {
	sources []agent.ToolSource
	logger  *zap.Logger

	mu    sync.RWMutex
	index map[string]agent.ToolSource
}

func NewMultiToolSource(logger *zap.Logger, sources ...agent.ToolSource) *MultiToolSource {
	if logger == nil {
		logger = zap.NewNop()
	}
	filtered := make([]agent.ToolSource, 0, len(sources))
	for _, src := range sources {
		if src != nil {
			filtered = append(filtered, src)
		}
	}
	return &MultiToolSource{sources: filtered, logger: logger}
}

// Tools merges every source's tools. A source that fails (e.g. the MCP
// gateway/sandbox source timing out) is skipped with a warning instead of
// failing the whole call, so /context keeps the always-available knowledge
// tools (search/fetch/content) even when the optional sandbox tools are
// briefly unreachable. Failing only when every source failed still surfaces
// a real "no tools at all" outage to Engine.Run's own (also non-fatal)
// handling of it.
func (m *MultiToolSource) Tools(ctx context.Context) ([]openai.ChatCompletionToolUnionParam, error) {
	tools := make([]openai.ChatCompletionToolUnionParam, 0)
	indexByName := make(map[string]agent.ToolSource)
	var anySucceeded bool
	for _, src := range m.sources {
		srcTools, err := src.Tools(ctx)
		if err != nil {
			m.logger.Warn("tool source unavailable, skipping its tools", zap.Error(err))
			continue
		}
		anySucceeded = true
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
	if !anySucceeded && len(m.sources) > 0 {
		return nil, errors.New("all tool sources unavailable")
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
