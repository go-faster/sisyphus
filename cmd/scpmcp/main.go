// Command scpmcp runs the MCP server exposing the knowledge base.
package main

import (
	"context"
	"flag"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/config"
	"github.com/go-faster/scpbot/internal/mcpserver"
	"github.com/go-faster/scpbot/internal/wire"
)

func main() {
	stdio := flag.Bool("stdio", false, "use stdio transport instead of Streamable HTTP")
	flag.Parse()

	app.Run(func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
		cfg, err := config.Load()
		if err != nil {
			return errors.Wrap(err, "config")
		}
		return run(ctx, lg, cfg, *stdio, t)
	})
}

func run(ctx context.Context, lg *zap.Logger, cfg config.Config, useStdio bool, t *app.Telemetry) error {
	comp, err := wire.New(ctx, lg, cfg, wire.NewOptions{
		TracerProvider: t.TracerProvider(),
		MeterProvider:  t.MeterProvider(),
	})
	if err != nil {
		return err
	}
	defer comp.Close()

	srv := mcpserver.New(comp.Retriever, comp.Answerer, mcpserver.Options{Logger: lg.Named("mcpserver")})
	if useStdio {
		lg.Info("mcp stdio starting")
		return srv.Run(ctx, &mcp.StdioTransport{})
	}

	// Streamable HTTP
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

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
