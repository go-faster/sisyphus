package main

import (
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/wire"
)

var (
	svc      *wire.Services
	cfg      config.Config
	globalTP trace.TracerProvider
	globalMP metric.MeterProvider
)

func newRoot(t *app.Telemetry) *cobra.Command {
	globalTP = t.TracerProvider()
	globalMP = t.MeterProvider()

	root := &cobra.Command{
		Use:           "ssingest",
		Short:         "ingest knowledge sources",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().Bool("dry-run", false, "fetch and log only; skip pipeline.Index")
	root.PersistentFlags().Int("limit", 0, "cap documents per source (0=unlimited)")
	root.PersistentFlags().String("reset", "none", "reset source|all|none before run")
	root.PersistentFlags().Bool("yes-i-mean-all", false, "confirm for --reset all")

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		lg := zctx.From(ctx)

		resetFlag, _ := cmd.Flags().GetString("reset")
		yesAll, _ := cmd.Flags().GetBool("yes-i-mean-all")
		if resetFlag == "all" && !yesAll {
			return errors.New("refusing --reset all without --yes-i-mean-all")
		}

		c, err := config.Load()
		if err != nil {
			return errors.Wrap(err, "config")
		}

		services, err := wire.NewServices(ctx, c, lg, globalTP, globalMP, false)
		if err != nil {
			return errors.Wrap(err, "setup services")
		}

		svc = services
		cfg = c
		return nil
	}

	root.PersistentPostRunE = func(_ *cobra.Command, _ []string) error {
		if svc != nil {
			svc.Close()
		}
		return nil
	}

	root.AddCommand(
		newGitCmd(),
		newGitLabCmd(),
		newJiraCmd(),
		newTelegramCmd(),
		newAllCmd(),
	)

	return root
}
