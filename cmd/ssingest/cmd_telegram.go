package main

import (
	"fmt"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	chunktg "github.com/go-faster/sisyphus/internal/chunk/telegram"
)

func newTelegramCmd(deps *ingestDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telegram [dump.json ...]",
		Short: "backfill Telegram chats; optional args are GDPR/Desktop chat export JSON files to ingest",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")
			doReset := resetFlag == "telegram" || resetFlag == "all"

			ch := chunktg.New()
			pipe, err := deps.pipeline(ch)
			if err != nil {
				return errors.Wrap(err, "build pipeline")
			}

			r := deps.runner()
			if err := r.runTelegram(ctx, pipe, time.Time{}, doReset, limit, dryRun, args); err != nil {
				if errors.Is(err, errNotConfigured) {
					fmt.Fprintf(os.Stderr, "telegram not configured or ingest session missing\n")
					os.Exit(1)
					return nil
				}
				return err
			}
			return nil
		},
	}
	return cmd
}
