package answer

import (
	"context"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/mcpclient"
)

// SSHToolSourceOptions configures the SSH MCP client wrapper.
type SSHToolSourceOptions struct{}

// NewSSHToolSource connects to an ssh-mcp server and returns it as an agent.ToolSource.
func NewSSHToolSource(ctx context.Context, sshMCPURL string, headers map[string]string, opts SSHToolSourceOptions) (agent.ToolSource, func(), error) {
	_ = opts
	if sshMCPURL == "" {
		return nil, nil, nil
	}
	client, err := mcpclient.New(ctx, mcpclient.Options{URL: sshMCPURL, Headers: headers})
	if err != nil {
		return nil, nil, err
	}
	return client, func() { _ = client.Close() }, nil
}
