// Command shits runs an MCP server that reads and edits a Google Sheets
// spreadsheet.
//
// It is deliberately standalone: unlike the other binaries here it needs no
// database, no Qdrant and no config.yaml, so it can be dropped into any MCP
// client as a stdio command. Flags fall back to environment variables.
package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/cliversion"
	"github.com/go-faster/sisyphus/internal/cmdutil"
	"github.com/go-faster/sisyphus/internal/httpmw"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/sheets"
	"github.com/go-faster/sisyphus/internal/sheetsmcp"
)

type options struct {
	credentials string
	spreadsheet string
	allow       []string
	readOnly    bool
	addr        string
	authToken   string
}

// quietTelemetry defaults the OTLP exporters off.
//
// app.Run builds them before our flags are parsed, and their default endpoint
// is localhost:4317 — so a stdio server, which an MCP client spawns fresh per
// session, dials a collector that is not there and then stalls on shutdown
// waiting for the upload to time out. An operator running the HTTP mode points
// these at a real collector explicitly, and that setting is honored.
func quietTelemetry() {
	for _, key := range []string{"OTEL_METRICS_EXPORTER", "OTEL_TRACES_EXPORTER", "OTEL_LOGS_EXPORTER"} {
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, "none")
		}
	}
}

func main() {
	quietTelemetry()
	app.Run(
		func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
			ctx = zctx.Base(ctx, lg)
			info, _ := cliversion.GetInfo("github.com/go-faster/sisyphus")

			var o options
			cmd := &cobra.Command{
				Use:   "shits",
				Short: "MCP server for reading and editing a Google Sheets spreadsheet",
				RunE: func(cmd *cobra.Command, _ []string) error {
					return run(cmd.Context(), o, t)
				},
				SilenceUsage:  true,
				SilenceErrors: true,
			}
			f := cmd.Flags()
			f.StringVar(&o.credentials, "credentials", env("SHITS_CREDENTIALS"),
				"path to a Google service account JSON key (default: application default credentials)")
			f.StringVar(&o.spreadsheet, "spreadsheet", env("SHITS_SPREADSHEET"),
				"default spreadsheet URL or ID used by calls that name none")
			f.StringSliceVar(&o.allow, "allow", envList("SHITS_ALLOW"),
				"spreadsheet URLs or IDs that may be addressed (default: any the credentials can reach)")
			f.BoolVar(&o.readOnly, "read-only", env("SHITS_READ_ONLY") != "",
				"expose only the read tools and request a read-only scope")
			f.StringVar(&o.addr, "http", env("SHITS_ADDR"),
				"serve Streamable HTTP on this address instead of stdio")
			f.StringVar(&o.authToken, "auth-token", env("SHITS_AUTH_TOKEN"),
				"bearer token required on /mcp in HTTP mode")

			cmdutil.ConfigureVersion(cmd, info)
			cmd.AddCommand(cmdutil.NewVersionCmd("shits", info))
			cmd.SetContext(ctx)
			return cmd.Execute()
		},
		app.WithServiceName("shits"),
		app.WithServiceNamespace("sisyphus"),
	)
}

func run(ctx context.Context, o options, t *app.Telemetry) error {
	lg := zctx.From(ctx)
	info, _ := cliversion.GetInfo("github.com/go-faster/sisyphus")

	client, err := sheets.New(ctx, sheets.Options{
		Logger:          lg.Named("sheets"),
		CredentialsFile: o.credentials,
		Default:         o.spreadsheet,
		Allow:           o.allow,
		ReadOnly:        o.readOnly,
	})
	if err != nil {
		return errors.Wrap(err, "sheets client")
	}
	lg.Info("sheets configured",
		zap.String("default", client.Default()),
		zap.Int("allowed", len(o.allow)),
		zap.Bool("read_only", o.readOnly),
	)

	srv := sheetsmcp.New(client, info.Short())
	if o.addr == "" {
		lg.Info("mcp stdio starting")
		return srv.Run(ctx, &mcp.StdioTransport{})
	}

	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	if o.authToken != "" {
		lg.Info("mcp auth enabled")
		handler = mcpserver.BearerAuthMiddleware(o.authToken)(handler)
	} else {
		lg.Warn("mcp auth disabled")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	s := &http.Server{
		Addr:              o.addr,
		Handler:           httpmw.Wrap(lg, t, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return httpmw.Serve(ctx, lg, "shits http", s)
}

func env(key string) string { return os.Getenv(key) }

func envList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}
