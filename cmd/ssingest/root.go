package main

import (
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/go-faster/sdk/zctx"
	"github.com/spf13/cobra"

	"github.com/go-faster/sisyphus/internal/cmdutil"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/wire"
)

func newRoot(t *app.Telemetry) *cobra.Command {
	deps := newIngestDeps(t)

	root := &cobra.Command{
		Use:           "ssingest",
		Short:         "ingest knowledge sources",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmdutil.ConfigureVersion(root, deps.info)
	root.AddCommand(cmdutil.NewVersionCmd("ssingest", deps.info))

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
		c.LogWarnings(lg)

		services, err := wire.NewServices(ctx, c, lg, deps.tp, deps.mp, deps.userAgent)
		if err != nil {
			return errors.Wrap(err, "setup services")
		}

		deps.services = services
		deps.cfg = c
		return nil
	}

	root.PersistentPostRunE = func(_ *cobra.Command, _ []string) error {
		if deps.services != nil {
			deps.services.Close()
			deps.services = nil
		}
		return nil
	}

	root.AddCommand(
		newGitCmd(deps),
		newFilesCmd(deps),
		newGitLabCmd(deps),
		newJiraCmd(deps),
		newTelegramCmd(deps),
		newAllCmd(deps),
		newIndexCmd(deps),
		newServeCmd(deps),
		newWorkerCmd(deps),
		newGCCmd(deps),
		newRepairCmd(deps),
	)

	return root
}
