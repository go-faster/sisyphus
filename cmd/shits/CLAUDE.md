# cmd/shits

An MCP server over a Google Sheets spreadsheet: `sheets_info`, `sheets_read`,
`sheets_write`, `sheets_append`, `sheets_clear`. `internal/sheets` is the API client,
`internal/sheetsmcp` the tool layer.

## It is deliberately not wired into `internal/config`

Every other binary here calls `config.Load()`, which **hard-fails without `database.dsn`**.
`shits` needs no database, no Qdrant and no ssapi, so requiring a DSN would make it
unrunnable as a plain stdio MCP command — which is its main use. Flags, with
`SHITS_*` env fallbacks, are the whole configuration surface. Do not "fix" this by moving
it onto the shared config unless the DSN requirement moves with it.

## Transport: stdio by default, HTTP on demand

Opposite of `ssmcp`, which serves HTTP and takes `--stdio`. Here `--http :8085` opts *in*
to Streamable HTTP; anything else is stdio. `--auth-token` guards `/mcp` and, as in
`ssmcp`, an empty token only warns.

```json
{"mcpServers": {"sheets": {"command": "shits", "env": {
  "SHITS_CREDENTIALS": "/path/sa.json",
  "SHITS_SPREADSHEET": "https://docs.google.com/spreadsheets/d/<id>/edit"
}}}}
```

## `quietTelemetry` runs before `app.Run` on purpose

`app.Run` builds the OTLP exporters before the command's flags are parsed, so the choice
cannot be made from `--http`. Defaulting `OTEL_{METRICS,TRACES,LOGS}_EXPORTER` to `none`
in `main` is what keeps a stdio session — spawned fresh by the MCP client every time —
from dialing a collector at `localhost:4317` and then blocking ~5s per exit on a shutdown
that will never succeed. An explicitly set variable is left alone, which is how the HTTP
deployment still exports. Moving this into `run` silently restores the stall.

## Access is bounded in three independent places

Each covers what the others cannot, so removing one is a real widening:

- **The service account only sees what is shared with it.** Nothing is reachable until
  someone shares the sheet with `…@….iam.gserviceaccount.com` (Editor, to write).
- **`--allow`** pins which spreadsheets may be addressed. Without it, *any* sheet shared
  with the account is fair game — including ones shared for unrelated reasons. A tool
  argument names the spreadsheet, so the model chooses the target; the allowlist is what
  keeps that choice inside a set an operator picked.
- **`--read-only`** switches the OAuth scope *and* drops the three mutating tools from
  the tool list. Both halves matter: the scope is the real enforcement, and hiding the
  tools stops the model from planning a write it will only be refused at the end.

`--spreadsheet` is checked against `--allow` in `sheets.New`, at startup rather than on
first call, so an inconsistent pair fails before the server is ever connected.

## Writes default to `USER_ENTERED`

A written `"1.5"` becomes a number and `"=SUM(A1:A2)"` a formula, exactly as if typed.
That is what makes editing a spreadsheet feel like editing a spreadsheet, but it means
cell text beginning with `=` is executed as a formula — pass `raw: true` for values that
must be stored literally.

## Reads are padded, writes are not

`toStrings` pads short rows to the widest one because the API drops trailing empty cells;
without it, row length tells a caller nothing and column indexes shift per row. The
padding is presentational: it is applied to what is read, never sent back on a write.
