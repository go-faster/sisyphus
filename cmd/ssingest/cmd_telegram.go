package main

import (
	"fmt"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"
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

			r := deps.runner()
			if err := r.runTelegram(ctx, time.Time{}, doReset, limit, dryRun, args); err != nil {
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
