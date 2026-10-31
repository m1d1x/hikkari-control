package analytics

import (
	"context"
	"fmt"
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
