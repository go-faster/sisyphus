package telegram

import (
	"context"
	"os"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
)

// DumpResult summarizes one GDPR-dump ingestion run across one or more files.
type DumpResult struct {
	Documents     []index.Document
	TotalMessages int
	TotalConvos   int
}

// IngestDump parses one or more Telegram chat export files (GDPR / Desktop
// "Export chat history" JSON, dump.json schema) at paths, persists their
// messages and grouped conversations, and returns Documents ready for
// pipeline.Index. Unlike the live gotd backfill, dumps are one-shot exports
// with no pagination cursor: each run re-walks the full file and relies on
// persistMessages/persistSupportRequests upserts and pipeline body-hash skip
// to stay idempotent.
func IngestDump(ctx context.Context, db *ent.Client, paths []string) (DumpResult, error) {
	var result DumpResult
	for _, path := range paths {
		if err := ingestDumpFile(ctx, db, path, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func ingestDumpFile(ctx context.Context, db *ent.Client, path string, result *DumpResult) error {
	f, err := os.Open(path) //nolint:gosec // path from operator config, not user input
	if err != nil {
		return errors.Wrapf(err, "open telegram dump %q", path)
	}
	defer func() { _ = f.Close() }()

	d, err := ParseDump(f)
	if err != nil {
		return errors.Wrapf(err, "parse telegram dump %q", path)
	}

	raw := d.RawMessages()
	if len(raw) == 0 {
		return nil
	}

	if err := persistMessages(ctx, db, raw); err != nil {
		return errors.Wrapf(err, "persist messages from %q", path)
	}

	groupMsgs := rawMessagesToMessages(raw)
	convs := Group(groupMsgs, DefaultGroupOptions())

	if err := persistSupportRequests(ctx, db, d.ID, convs); err != nil {
		return errors.Wrapf(err, "persist support requests from %q", path)
	}

	for _, conv := range convs {
		result.Documents = append(result.Documents, DocumentFromConversation(conv, "", ""))
	}
	result.TotalMessages += len(raw)
	result.TotalConvos += len(convs)
	return nil
}
