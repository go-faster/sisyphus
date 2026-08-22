package telemetry

import (
	"database/sql"

	"github.com/XSAM/otelsql"
	"github.com/go-faster/errors"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// RegisterDBStats reports the sql.DB pool gauges — open, idle, in-use, wait
// count and wait duration — as metrics.
//
// otelsql.Open only produces query spans, and those cannot show pool
// saturation: a caller blocked waiting for a free connection has no span to
// be slow in, so the pool filling up looks like nothing at all.
//
// The returned function unregisters the callback. Call it before closing the
// pool, otherwise the collector keeps polling a closed DB.
func RegisterDBStats(db *sql.DB) (func(), error) {
	reg, err := otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(semconv.DBSystemPostgreSQL))
	if err != nil {
		return nil, errors.Wrap(err, "register db stats metrics")
	}
	return func() { _ = reg.Unregister() }, nil
}
