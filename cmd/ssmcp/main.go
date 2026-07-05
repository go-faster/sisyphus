// Command ssmcp runs the MCP server exposing the knowledge base.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/apiclient"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/netclient"
)

func main() {
	app.Run(
		func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
			ctx = zctx.Base(ctx, lg)
			var stdio bool
			cmd := &cobra.Command{
				Use:   "ssmcp",
				Short: "runs the MCP server exposing the knowledge base",
				RunE: func(cmd *cobra.Command, _ []string) error {
					cfg, err := config.Load()
					if err != nil {
						return errors.Wrap(err, "config")
					}
					return run(cmd.Context(), cfg, stdio, t)
				},
				SilenceUsage:  true,
				SilenceErrors: true,
			}
			cmd.Flags().BoolVar(&stdio, "stdio", false, "use stdio transport instead of Streamable HTTP")
			cmd.SetContext(ctx)
			return cmd.Execute()
		},
		app.WithServiceName("ssmcp"),
		app.WithServiceNamespace("sisyphus"),
	)
}

func run(ctx context.Context, cfg config.Config, useStdio bool, t *app.Telemetry) error {
	lg := zctx.From(ctx)
	if cfg.API.BaseURL == "" || cfg.API.AuthToken == "" {
		return errors.New("api.base_url and api.auth_token are required")
	}

	httpClient, err := netclient.HTTPClient(ctx, "ssapi", "", netclient.HTTPClientOptions{
		TracerProvider: t.TracerProvider(),
		MeterProvider:  t.MeterProvider(),
	})
	if err != nil {
		return errors.Wrap(err, "http client")
	}

	api, err := apiclient.New(cfg.API.BaseURL, cfg.API.AuthToken, apiclient.Options{
		HTTPClient:     httpClient,
		TracerProvider: t.TracerProvider(),
		MeterProvider:  t.MeterProvider(),
	})
	if err != nil {
		return errors.Wrap(err, "api client")
	}

	srv := mcpserver.New(api, api)
	if useStdio {
		lg.Info("mcp stdio starting")
		return srv.Run(ctx, &mcp.StdioTransport{})
	}

	if cfg.MCPAuthToken == "" {
		return errors.New("mcp_auth_token is required for HTTP transport")
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.BearerAuthMiddleware(cfg.MCPAuthToken)(handler))

	lg.Info("mcp http listening", zap.String("addr", cfg.MCPAddr))
	s := &http.Server{
		Addr:              cfg.MCPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- errors.Wrap(err, "http serve")
		}
	}()

	select {
	case <-ctx.Done():
	case e := <-errc:
		_ = shutdown(s)
		return e
	}
	return shutdown(s)
}

func shutdown(s *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.Shutdown(ctx)
}
