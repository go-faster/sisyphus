package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Call invokes a specific tool on the MCP server.
func (c *Client) Call(ctx context.Context, name string, argsJSON json.RawMessage) (_ string, rerr error) {
	start := time.Now()
	defer func() {
		if c.m != nil {
			c.m.record(ctx, name, time.Since(start).Seconds(), rerr)
		}
	}()

	var args map[string]any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return "", errors.Wrap(err, "unmarshal tool args")
		}
	}

	req := mcp.CallToolParams{}
	req.Name = name
	req.Arguments = args

	res, err := withSession(ctx, c, func(session *mcp.ClientSession) (*mcp.CallToolResult, error) {
		return session.CallTool(ctx, &req)
	})
	if err != nil {
		return "", errors.Wrap(err, "call tool")
	}

	if res.IsError {
		// Try to extract an error message from the text content.
		var errMsgs []string
		for _, content := range res.Content {
			if tc, ok := content.(*mcp.TextContent); ok {
				errMsgs = append(errMsgs, tc.Text)
			}
		}
		if len(errMsgs) > 0 {
			return "", errors.Errorf("tool returned error: %s", strings.Join(errMsgs, "; "))
		}
		return "", errors.New("tool returned an unspecified error")
	}

	// Flatten contents into a single string.
	var sb strings.Builder
	for i, content := range res.Content {
		if i > 0 {
			sb.WriteString("\n")
		}
		switch v := content.(type) {
		case *mcp.TextContent:
			sb.WriteString(v.Text)
		case *mcp.ImageContent:
			fmt.Fprintf(&sb, "[image: %s]", v.MIMEType)
		case *mcp.EmbeddedResource:
			fmt.Fprintf(&sb, "[embedded resource: %s]", v.Resource.URI)
		default:
			fmt.Fprintf(&sb, "[unknown content type: %T]", content)
		}
	}

	return sb.String(), nil
}
