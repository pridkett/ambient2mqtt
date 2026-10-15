package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TimescaleDB settings. The connection comes from the standard PG*
// environment variables (PGHOST, PGUSER, PGPASSWORD, PGDATABASE, ...), so
// the config file holds no credentials.
type tomlConfigTimescale struct {
	Table string
	// Station names this weather station in the station column.
	Station string
	// MaxQueue caps the reports held while the database is unreachable.
	MaxQueue int
}

var errSchema = errors.New("schema mismatch")

// Report keys that never become columns: the station's MAC address and its
// own clock (rows are timestamped on receipt, as in InfluxDB).
var pgSkipKeys = map[string]bool{"PASSKEY": true, "dateutc": true}

type pgColumn struct {
	name, dataType string
}

type pgReport struct {
	t      time.Time
	values url.Values
}

// pgWriter inserts each report as one row of a wide table. The station sends
// whichever readings it has, so rows are built from the table's own columns:
// a report key fills the column of the same name, and a column the report
// lacks is NULL. Reports that can't be written are queued and retried.
type pgWriter struct {
	pool     *pgxpool.Pool
	table    string
	station  string
	maxQueue int

	mu      sync.Mutex
	columns []pgColumn // loaded on the first write
	queue   []pgReport
	unknown map[string]bool
}

func newPGWriter(ctx context.Context, cfg tomlConfigTimescale) (*pgWriter, error) {
	if cfg.Table == "" || cfg.Station == "" {
		return nil, fmt.Errorf("timescale: table and station are required")
	}
	pgcfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("timescale: %w", err)
	}
	pgcfg.ConnConfig.RuntimeParams["application_name"] = "ambient2mqtt"
	pgcfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, pgcfg)
	if err != nil {
		return nil, fmt.Errorf("timescale: %w", err)
	}
	maxQueue := cfg.MaxQueue
	if maxQueue <= 0 {
		maxQueue = 2000 // over a day at the station's 1 min reports
	}
	return &pgWriter{pool: pool, table: cfg.Table, station: cfg.Station, maxQueue: maxQueue, unknown: map[string]bool{}}, nil
}

// loadColumns reads the table's columns. It must have time and station.
func (w *pgWriter) loadColumns(ctx context.Context) error {
	rows, err := w.pool.Query(ctx, `
		SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1
		ORDER BY ordinal_position`, w.table)
	if err != nil {
		return err
	}
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pgColumn, error) {
		var c pgColumn
		return c, r.Scan(&c.name, &c.dataType)
	})
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("%w: table %q doesn't exist", errSchema, w.table)
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[c.name] = true
	}
	if !have["time"] || !have["station"] {
		return fmt.Errorf("%w: table %q needs time and station columns", errSchema, w.table)
	}
	w.columns = cols
	return nil
}

// pgValue converts a report value for a column; ok is false if it can't.
func pgValue(v, dataType string) (any, bool) {
	switch dataType {
	case "text", "character varying":
		return v, true
	case "integer", "smallint", "bigint":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		return int64(math.Round(f)), true
	default: // double precision, real, numeric
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		return f, true
	}
}

// row builds a report's values in column order.
func (w *pgWriter) row(rep pgReport) []any {
	row := make([]any, len(w.columns))
	used := map[string]bool{}
	for i, c := range w.columns {
		switch c.name {
		case "time":
			row[i] = rep.t
			continue
		case "station":
			row[i] = w.station
			continue
		}
		v := rep.values.Get(c.name)
		if v == "" || pgSkipKeys[c.name] {
			continue
		}
		used[c.name] = true
		if val, ok := pgValue(v, c.dataType); ok {
			row[i] = val
		} else {
			logger.Warnf("TimescaleDB: can't store %s=%q in a %s column, leaving it NULL", c.name, v, c.dataType)
		}
	}
	for k := range rep.values {
		if !used[k] && !pgSkipKeys[k] && !w.unknown[k] {
			w.unknown[k] = true
			logger.Warnf("TimescaleDB: table %s has no column for %q; not storing it", w.table, k)
		}
	}
	return row
}

// write queues a report and flushes the queue. It returns the number of rows
// written; on error the reports stay queued for the next call.
func (w *pgWriter) write(ctx context.Context, t time.Time, values url.Values) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queue = append(w.queue, pgReport{t, values})
	if over := len(w.queue) - w.maxQueue; over > 0 {
		logger.Warnf("TimescaleDB queue full, dropping the %d oldest reports", over)
		w.queue = w.queue[over:]
	}
	if w.columns == nil {
		if err := w.loadColumns(ctx); err != nil {
			return 0, err
		}
	}
	names := make([]string, len(w.columns))
	for i, c := range w.columns {
		names[i] = c.name
	}
	rows := make([][]any, len(w.queue))
	for i, rep := range w.queue {
		rows[i] = w.row(rep)
	}
	n, err := w.pool.CopyFrom(ctx, pgx.Identifier{w.table}, names, pgx.CopyFromRows(rows))
	if err != nil {
		return 0, err
	}
	w.queue = w.queue[:0]
	return int(n), nil
}
