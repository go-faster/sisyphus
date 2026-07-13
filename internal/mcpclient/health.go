package mcpclient

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CheckHealth checks the connection to the MCP server.
func (c *Client) CheckHealth(ctx context.Context) error {
	// A simple tool listing serves as a health check to verify communication
	_, err := withSession(ctx, c, func(session *mcp.ClientSession) (*mcp.ListToolsResult, error) {
		return session.ListTools(ctx, nil)
	})
	if err != nil {
		return errors.Wrap(err, "health check failed")
	}
	return nil
}
