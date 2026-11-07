package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

// Client telemetry (hikkari-client docs/TELEMETRY.md §9): raw rows live 90
// days; daily rollups keep only counts and a mergeable unique-install sketch,
// so actives over any span stay answerable after the raw rows are gone,
// without keeping a single install ID.
var telemetrySchema = []string{
	`CREATE TABLE IF NOT EXISTS telemetry_events (
		ts       DateTime CODEC(ZSTD(1)),
		install  String CODEC(ZSTD(1)),
		version  LowCardinality(String),
		channel  LowCardinality(String),
		os       LowCardinality(String),
		os_major LowCardinality(String),
		arch     LowCardinality(String),
		name     LowCardinality(String),
		engine   LowCardinality(String),
		fields   String CODEC(ZSTD(1))
	) ENGINE = MergeTree()
	PARTITION BY toYYYYMMDD(ts)
	ORDER BY (name, ts)
	TTL ts + INTERVAL 90 DAY`,
	`CREATE TABLE IF NOT EXISTS telemetry_errors (
		ts        DateTime CODEC(ZSTD(1)),
		install   String CODEC(ZSTD(1)),
		version   LowCardinality(String),
		channel   LowCardinality(String),
		os        LowCardinality(String),
		os_major  LowCardinality(String),
		arch      LowCardinality(String),
		code      LowCardinality(String),
		component LowCardinality(String),
		fp        FixedString(16),
		msg       String CODEC(ZSTD(1)),
		count     UInt32
	) ENGINE = MergeTree()
	PARTITION BY toYYYYMMDD(ts)
	ORDER BY (code, fp, ts)
	TTL ts + INTERVAL 90 DAY`,
	`CREATE TABLE IF NOT EXISTS telemetry_daily_events (
		day      Date,
		version  LowCardinality(String),
		os       LowCardinality(String),
		engine   LowCardinality(String),
		name     LowCardinality(String),
		installs AggregateFunction(uniq, String),
		events   SimpleAggregateFunction(sum, UInt64)
	) ENGINE = AggregatingMergeTree()
	ORDER BY (day, name, version, os, engine)`,
	`CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_daily_events_mv TO telemetry_daily_events AS
	SELECT toDate(ts) AS day, version, os, engine, name,
		uniqState(install) AS installs, toUInt64(count()) AS events
	FROM telemetry_events
	GROUP BY day, version, os, engine, name`,
	`CREATE TABLE IF NOT EXISTS telemetry_daily_errors (
		day         Date,
		version     LowCardinality(String),
		os          LowCardinality(String),
		component   LowCardinality(String),
		code        LowCardinality(String),
		installs    AggregateFunction(uniq, String),
		occurrences SimpleAggregateFunction(sum, UInt64)
	) ENGINE = AggregatingMergeTree()
	ORDER BY (day, code, version, os, component)`,
	`CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_daily_errors_mv TO telemetry_daily_errors AS
	SELECT toDate(ts) AS day, version, os, component, code,
		uniqState(install) AS installs, toUInt64(sum(count)) AS occurrences
	FROM telemetry_errors
	GROUP BY day, version, os, component, code`,
	`CREATE TABLE IF NOT EXISTS landing_downloads (
		ts   DateTime CODEC(ZSTD(1)),
		os   LowCardinality(String),
		file LowCardinality(String)
	) ENGINE = MergeTree()
	PARTITION BY toYYYYMMDD(ts)
	ORDER BY (os, ts)
	TTL ts + INTERVAL 90 DAY`,
}

func (s *Store) migrateTelemetry(ctx context.Context) error {
	for i, ddl := range telemetrySchema {
		if err := s.conn.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("telemetry schema %d: %w", i+1, err)
		}
	}
	return nil
}

// InsertTelemetry writes one accepted client batch. Synchronous, unlike the
// fleet's pulses: the hub answers 204 only once the rows are durable, and the
// client deletes its copy on 204.
func (s *Store) InsertTelemetry(ctx context.Context, events []telemetry.EventRow, errs []telemetry.ErrorRow) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 1,
	}))
	if len(events) > 0 {
		batch, err := s.conn.PrepareBatch(ctx,
			"INSERT INTO telemetry_events (ts, install, version, channel, os, os_major, arch, name, engine, fields)")
		if err != nil {
			return fmt.Errorf("prepare telemetry_events: %w", err)
		}
		for _, e := range events {
			if err := batch.Append(e.TS, e.Install, e.App.Version, e.App.Channel, e.App.OS, e.App.OSMajor,
				e.App.Arch, e.Name, e.Engine, e.Fields); err != nil {
				return fmt.Errorf("append telemetry event: %w", err)
			}
		}
		if err := batch.Send(); err != nil {
			return fmt.Errorf("send telemetry_events: %w", err)
		}
	}
	// An attempt to connect is an attempt to reach the service. Derived here
	// and not in the hub's request path: the doorway dataset is one view of
	// what clients already reported, and its failure must not make the client
	// resend (and duplicate) a batch the events table already holds.
	if rows := ReachFromTelemetry(events); len(rows) > 0 {
		if err := s.InsertEntryReach(ctx, rows); err != nil {
			log.Printf("analytics: reach from telemetry: %v", err)
		}
	}
	if len(errs) > 0 {
		batch, err := s.conn.PrepareBatch(ctx,
			"INSERT INTO telemetry_errors (ts, install, version, channel, os, os_major, arch, code, component, fp, msg, count)")
		if err != nil {
			return fmt.Errorf("prepare telemetry_errors: %w", err)
		}
		for _, e := range errs {
			if err := batch.Append(e.TS, e.Install, e.App.Version, e.App.Channel, e.App.OS, e.App.OSMajor,
				e.App.Arch, e.Code, e.Component, e.FP, e.Msg, e.Count); err != nil {
				return fmt.Errorf("append telemetry error: %w", err)
			}
		}
		if err := batch.Send(); err != nil {
			return fmt.Errorf("send telemetry_errors: %w", err)
		}
	}
	return nil
}

// InsertLandingDownload writes one download click from the landing page.
func (s *Store) InsertLandingDownload(ctx context.Context, ts time.Time, os, file string) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 1,
	}))
	if err := s.conn.Exec(ctx, "INSERT INTO landing_downloads (ts, os, file) VALUES (?, ?, ?)", ts, os, file); err != nil {
		return fmt.Errorf("insert landing download: %w", err)
	}
	return nil
}

// ProtoRelayPrefix marks an entry_reach row whose relay_name is not a relay of
// ours but the protocol a client connected over: a connect event names the
// protocol, never the machine. The overview reads what follows as the protocol.
const ProtoRelayPrefix = "proto:"

// ReachAccount is the pseudonym an install is counted under in entry_reach:
// the doorway console counts people and must not hold a list of who they are.
func ReachAccount(install string) string {
	sum := sha256.Sum256([]byte("reach:" + install))
	return hex.EncodeToString(sum[:16])
}

// reachProto names a telemetry protocol the way the pool does.
func reachProto(p string) string {
	switch p {
	case "":
		return "other"
	case "vless-reality":
		return "vless-reality-vision"
	}
	return p
}

// ReachFromTelemetry turns the batch's connect events into doorway rows: one
// event, one attempt, counted for the install that made it. The wait is the
// event's own seconds; the round trip is not measured by a connect, so it
// stays zero.
func ReachFromTelemetry(events []telemetry.EventRow) []EntryReachRecord {
	var out []EntryReachRecord
	for _, e := range events {
		if e.Name != "connect" {
			continue
		}
		var f struct {
			OK    bool   `json:"ok"`
			Proto string `json:"proto"`
			Secs  uint32 `json:"secs"`
		}
		if json.Unmarshal([]byte(e.Fields), &f) != nil {
			continue
		}
		r := EntryReachRecord{
			Timestamp:   e.TS,
			Account:     ReachAccount(e.Install),
			Outcome:     "failed",
			RelaysTried: 1,
			ConnectMs:   min(f.Secs, 600) * 1000,
		}
		if f.OK {
			r.Outcome, r.RelayName = "relay", ProtoRelayPrefix+reachProto(f.Proto)
		}
		out = append(out, r)
	}
	return out
}

// BackfillReachFromTelemetry carries the connect events already stored into
// entry_reach, with the same derivation as [ReachFromTelemetry]. Returns how
// many events it read.
func (s *Store) BackfillReachFromTelemetry(ctx context.Context) (int, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT ts, install, fields FROM telemetry_events WHERE name = 'connect' ORDER BY ts`)
	if err != nil {
		return 0, fmt.Errorf("read connect events: %w", err)
	}
	defer rows.Close()
	var events []telemetry.EventRow
	for rows.Next() {
		e := telemetry.EventRow{Name: "connect"}
		if err := rows.Scan(&e.TS, &e.Install, &e.Fields); err != nil {
			return 0, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return len(events), s.InsertEntryReach(ctx, ReachFromTelemetry(events))
}
