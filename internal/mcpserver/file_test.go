package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

type fakeContentResolver struct {
	resp index.ContentResponse
	err  error
}

func (f *fakeContentResolver) ResolveContent(ctx context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	return f.resp, f.err
}

func TestFileHandler_NilResolver(t *testing.T) {
	handler := fileHandler(nil)
	ctx := t.Context()

	result, out, err := handler(ctx, nil, FileArgs{})

	require.Nil(t, err, "Go error should be nil")
	require.NotNil(t, result, "CallToolResult should not be nil")
	require.True(t, result.IsError, "CallToolResult should be error")
	require.Equal(t, "file content resolver not configured", result.Content[0].(*mcp.TextContent).Text)
	require.Equal(t, FileOut{}, out, "output should be empty")
}

func TestFileHandler_ResolveContentError(t *testing.T) {
	resolver := &fakeContentResolver{err: index.ErrURLNotAllowed}
	handler := fileHandler(resolver)
	ctx := t.Context()

	args := FileArgs{
		Repo: "test-repo",
		Path: "test.txt",
	}

	result, out, err := handler(ctx, nil, args)

	require.Nil(t, err, "Go error should be nil")
	require.NotNil(t, result, "CallToolResult should not be nil")
	require.True(t, result.IsError, "CallToolResult should be error")
	require.Equal(t, "resolve content: url not in allowlist", result.Content[0].(*mcp.TextContent).Text)
	require.Equal(t, FileOut{}, out, "output should be empty")
}

func TestFileHandler_FileNotFound(t *testing.T) {
	resolver := &fakeContentResolver{
		resp: index.ContentResponse{Found: false},
	}
	handler := fileHandler(resolver)
	ctx := t.Context()

	args := FileArgs{
		Repo: "test-repo",
		Path: "nonexistent.txt",
	}

	result, out, err := handler(ctx, nil, args)

	require.Nil(t, err, "Go error should be nil")
	require.NotNil(t, result, "CallToolResult should not be nil")
	require.True(t, result.IsError, "CallToolResult should be error")
	require.Equal(t, "file not found", result.Content[0].(*mcp.TextContent).Text)
	require.Equal(t, FileOut{}, out, "output should be empty")
}

func TestFileHandler_SuccessfulResolve(t *testing.T) {
	resp := index.ContentResponse{
		Content: "func main() {\n  fmt.Println(\"hello\")\n}",
		Source:  "local_clone",
		Found:   true,
	}
	resolver := &fakeContentResolver{resp: resp}
	handler := fileHandler(resolver)
	ctx := t.Context()

	args := FileArgs{
		Repo:   "test-repo",
		Path:   "main.go",
		Branch: "main",
		Start:  1,
		End:    3,
	}

	result, out, err := handler(ctx, nil, args)

	require.Nil(t, err, "Go error should be nil")
	require.Nil(t, result, "CallToolResult should be nil on success")
	require.Equal(t, "func main() {\n  fmt.Println(\"hello\")\n}", out.Content)
	require.Equal(t, "local_clone", out.Source)
	require.True(t, out.Found)
}

func TestFileHandler_PassesThroughArgs(t *testing.T) {
	var capturedReq index.ContentRequest
	resolver := &capturingResolver{
		captured: &capturedReq,
		resp: index.ContentResponse{
			Content: "test",
			Source:  "database",
			Found:   true,
		},
	}
	handler := fileHandler(resolver)
	ctx := t.Context()

	args := FileArgs{
		Repo:   "my-repo",
		Path:   "src/index.ts",
		Branch: "feature/test",
		Start:  10,
		End:    20,
	}

	result, _, err := handler(ctx, nil, args)

	require.Nil(t, err)
	require.Nil(t, result)
	require.Equal(t, "my-repo", capturedReq.Repo)
	require.Equal(t, "src/index.ts", capturedReq.Path)
	require.Equal(t, "feature/test", capturedReq.Branch)
	require.Equal(t, 10, capturedReq.Start)
	require.Equal(t, 20, capturedReq.End)
}

type capturingResolver struct {
	captured *index.ContentRequest
	resp     index.ContentResponse
	err      error
}

func (f *capturingResolver) ResolveContent(ctx context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	*f.captured = req
	return f.resp, f.err
}
