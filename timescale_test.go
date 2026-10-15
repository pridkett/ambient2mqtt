package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/withmandala/go-log"
)

func TestMain(m *testing.M) {
	logger = log.New(os.Stderr)
	os.Exit(m.Run())
}

// A WS-2902 report as the station sends it, captured 2026-10-15 (passkey replaced).
const report = "stationtype=AMBWeatherV4.3.7&PASSKEY=00:00:00:00:00:00&dateutc=2026-10-15+18:02:31" +
	"&tempinf=66.0&humidityin=49&baromrelin=29.980&baromabsin=29.622&tempf=61.2&humidity=46&winddir=320" +
	"&windspeedmph=3.4&windgustmph=4.5&maxdailygust=23.9&hourlyrainin=0.000&eventrainin=0.000&dailyrainin=0.000" +
	"&weeklyrainin=2.201&monthlyrainin=2.319&totalrainin=86.429&solarradiation=417.66&uv=4"

func mustQuery(t *testing.T, q string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPGValue(t *testing.T) {
	for _, c := range []struct {
		v, typ string
		want   any
		ok     bool
	}{
		{"61.2", "double precision", 61.2, true},
		{"46", "integer", int64(46), true},
		{"4.0", "integer", int64(4), true},
		{"AMBWeatherV4.3.7", "text", "AMBWeatherV4.3.7", true},
		{"n/a", "double precision", nil, false},
		{"NaN", "double precision", nil, false},
	} {
		got, ok := pgValue(c.v, c.typ)
		if got != c.want || ok != c.ok {
			t.Errorf("pgValue(%q, %s) = %v, %v; want %v, %v", c.v, c.typ, got, ok, c.want, c.ok)
		}
	}
}

func TestRow(t *testing.T) {
	w := &pgWriter{table: "weather", station: "edgewater", unknown: map[string]bool{}, columns: []pgColumn{
		{"time", "timestamp with time zone"}, {"station", "text"}, {"stationtype", "text"},
		{"tempf", "double precision"}, {"humidity", "integer"}, {"batt_co2", "integer"}, {"PASSKEY", "text"},
	}}
	ts := time.Date(2026, 10, 15, 18, 2, 32, 0, time.UTC)
	row := w.row(pgReport{ts, mustQuery(t, report)})
	want := []any{ts, "edgewater", "AMBWeatherV4.3.7", 61.2, int64(46), nil, nil}
	for i := range want {
		if row[i] != want[i] {
			t.Errorf("%s = %v, want %v", w.columns[i].name, row[i], want[i])
		}
	}
	// keys without a column are noted once, never the passkey or the station clock
	if !w.unknown["tempinf"] || w.unknown["PASSKEY"] || w.unknown["dateutc"] {
		t.Errorf("unknown keys %v", w.unknown)
	}
}

// Without a database, reports queue up to MaxQueue, dropping the oldest.
func TestQueueWhileDown(t *testing.T) {
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGCONNECT_TIMEOUT", "1")
	w, err := newPGWriter(context.Background(), tomlConfigTimescale{Table: "weather", Station: "edgewater", MaxQueue: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if _, err := w.write(context.Background(), time.Unix(int64(i), 0), mustQuery(t, report)); err == nil {
			t.Fatal("write succeeded with no database")
		}
	}
	if len(w.queue) != 3 || w.queue[0].t != time.Unix(2, 0) {
		t.Errorf("queue has %d reports, first at %v", len(w.queue), w.queue[0].t)
	}
}

// TestTimescale runs against a real database when PGHOST is set, e.g. a
// local timescale/timescaledb container.
func TestTimescale(t *testing.T) {
	if os.Getenv("PGHOST") == "" {
		t.Skip("PGHOST not set")
	}
	ctx := context.Background()
	w, err := newPGWriter(ctx, tomlConfigTimescale{Table: "weather_test", Station: "edgewater"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.pool.Exec(ctx, `DROP TABLE IF EXISTS weather_test`)

	if _, err := w.write(ctx, time.Now(), mustQuery(t, report)); !errors.Is(err, errSchema) {
		t.Fatalf("missing table: err = %v", err)
	}
	w.queue = nil
	if _, err := w.pool.Exec(ctx, `CREATE TABLE weather_test (time timestamptz, tempf double precision)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.write(ctx, time.Now(), mustQuery(t, report)); !errors.Is(err, errSchema) {
		t.Fatalf("no station column: err = %v", err)
	}
	w.queue = nil
	if _, err := w.pool.Exec(ctx, `DROP TABLE weather_test; `+testSchema); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 10, 15, 18, 2, 32, 0, time.UTC)
	if n, err := w.write(ctx, ts, mustQuery(t, report)); err != nil || n != 1 {
		t.Fatalf("write = %d, %v", n, err)
	}
	var got time.Time
	var station, stype string
	var tempf, rain float64
	var humidity, uv int
	var batt *int
	err = w.pool.QueryRow(ctx, `SELECT time, station, stationtype, tempf, humidity, uv, totalrainin, batt_co2 FROM weather_test`).
		Scan(&got, &station, &stype, &tempf, &humidity, &uv, &rain, &batt)
	if err != nil || !got.Equal(ts) || station != "edgewater" || stype != "AMBWeatherV4.3.7" || tempf != 61.2 ||
		humidity != 46 || uv != 4 || rain != 86.429 || batt != nil {
		t.Errorf("row = %v %s %s %v %d %d %v %v, %v", got, station, stype, tempf, humidity, uv, rain, batt, err)
	}
}

// The weather table from ansible-config's environment.sql.j2.
const testSchema = `CREATE TABLE weather_test (
  time timestamptz NOT NULL, station text NOT NULL, stationtype text,
  tempf double precision, tempinf double precision, humidity integer, humidityin integer,
  baromrelin double precision, baromabsin double precision, winddir integer,
  windspeedmph double precision, windgustmph double precision, maxdailygust double precision,
  hourlyrainin double precision, eventrainin double precision, dailyrainin double precision,
  weeklyrainin double precision, monthlyrainin double precision, totalrainin double precision,
  solarradiation double precision, uv integer, batt_co2 integer)`
