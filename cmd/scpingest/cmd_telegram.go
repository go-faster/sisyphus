package main

import (
	"fmt"
	"os"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	chunktg "github.com/go-faster/scpbot/internal/chunk/telegram"
	"github.com/go-faster/scpbot/internal/pipeline"
)

func newTelegramCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telegram",
		Short: "backfill Telegram chats",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			limit, _ := cmd.Flags().GetInt("limit")
			resetFlag, _ := cmd.Flags().GetString("reset")
			doReset := resetFlag == "telegram" || resetFlag == "all"

			ch := chunktg.New()
			pipe, err := pipeline.New(svc.DB, ch, svc.Embedder, svc.Vectors, pipeline.PipelineOptions{
				TracerProvider: globalTP,
				MeterProvider:  globalMP,
			})
			if err != nil {
				return errors.Wrap(err, "build pipeline")
			}

			r := runner{
				db:      svc.DB,
				vectors: svc.Vectors,
				cfg:     cfg,
				tp:      globalTP,
				mp:      globalMP,
			}
			if err := r.runTelegram(ctx, pipe, time.Time{}, doReset, limit, dryRun); err != nil {
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
