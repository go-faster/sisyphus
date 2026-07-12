package answer

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/mcpclient"
	"github.com/go-faster/sisyphus/internal/netclient"
)

// SSHToolSourceOptions configures the SSH MCP client wrapper.
type SSHToolSourceOptions struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	UserAgent      string
	HTTPClient     *http.Client
}

func (opts *SSHToolSourceOptions) setDefaults() {
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.UserAgent == "" {
		if info, ok := cliversion.GetInfo("github.com/go-faster/sisyphus"); ok {
			opts.UserAgent = info.UserAgent("ssapi")
		}
	}
}

// NewSSHToolSource connects to an ssh-mcp server and returns it as an agent.ToolSource.
func NewSSHToolSource(ctx context.Context, sshMCPURL string, headers map[string]string, opts SSHToolSourceOptions) (agent.ToolSource, func(), error) {
	opts.setDefaults()
	if sshMCPURL == "" {
		return nil, nil, nil
	}
	if opts.HTTPClient == nil {
		client, err := netclient.HTTPClient(ctx, "ssh-mcp", "", netclient.HTTPClientOptions{
			TracerProvider: opts.TracerProvider,
			MeterProvider:  opts.MeterProvider,
			UserAgent:      opts.UserAgent,
		})
		if err != nil {
			return nil, nil, err
		}
		opts.HTTPClient = client
	}
	client, err := mcpclient.New(ctx, mcpclient.Options{
		URL:        sshMCPURL,
		Headers:    headers,
		HTTPClient: opts.HTTPClient,
		Version:    func() string { info, _ := cliversion.GetInfo("github.com/go-faster/sisyphus"); return info.Short() }(),
	})
	if err != nil {
		return nil, nil, err
	}
	return client, func() { _ = client.Close() }, nil
}
