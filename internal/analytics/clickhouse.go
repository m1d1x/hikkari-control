package analytics

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Store manages ClickHouse connections and event writes.
type Store struct {
	conn driver.Conn
}

// NewStore connects to ClickHouse using the given DSN.
// DSN format: clickhouse://user:password@host:9000/database
func NewStore(dsn string) (*Store, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	opts.MaxOpenConns = 20
	opts.MaxIdleConns = 5
	opts.ConnMaxLifetime = 10 * time.Minute
	opts.DialTimeout = 5 * time.Second

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}

	log.Println("Connected to ClickHouse")
	return &Store{conn: conn}, nil
}

// Close releases the ClickHouse connection.
func (s *Store) Close() error {
	return s.conn.Close()
}

// Ping checks connectivity to ClickHouse.
func (s *Store) Ping(ctx context.Context) error {
	return s.conn.Ping(ctx)
}

// QueryRow executes a query and scans a single row result.
func (s *Store) QueryRow(ctx context.Context, dest any, query string, args ...any) error {
	return s.conn.QueryRow(ctx, query, args...).Scan(dest)
}

// Query executes a query and returns rows via the driver.
func (s *Store) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	return s.conn.Query(ctx, query, args...)
}

// Exec runs a statement that does not return rows (DDL, TRUNCATE, ALTER ... DELETE).
func (s *Store) Exec(ctx context.Context, query string, args ...any) error {
	return s.conn.Exec(ctx, query, args...)
}

// RunMigrations creates the sessions table and applies fraud columns.
func (s *Store) RunMigrations(ctx context.Context) error {
	// 001: base sessions table
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS sessions (
			session_id      UUID,
			user_id         UUID,
			started_at      DateTime,
			referrer        String DEFAULT '',
			utm_source      String DEFAULT '',
			utm_medium      String DEFAULT '',
			utm_campaign    String DEFAULT '',
			ip_address      String DEFAULT '',
			country_code    String DEFAULT '',
			country_name    String DEFAULT '',
			region          String DEFAULT '',
			city            String DEFAULT '',
			browser         String DEFAULT '',
			browser_version String DEFAULT '',
			os              String DEFAULT '',
			os_version      String DEFAULT '',
			device_type     String DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMM(started_at)
		ORDER BY (started_at, user_id)
	`); err != nil {
		return fmt.Errorf("migration 001: %w", err)
	}

	// 002: fraud detection columns
	fraudCols := []string{
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS asn_number    UInt32  DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS asn_org       String  DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS is_datacenter UInt8   DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS is_vpn        UInt8   DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS is_proxy      UInt8   DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS is_tor        UInt8   DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS is_hosting    UInt8   DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS pow_solved    UInt8   DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS pow_time_ms   UInt32  DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS fp_hash       String  DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS risk_score    UInt8   DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN IF NOT EXISTS behavior_signals String DEFAULT ''",
	}
	for _, q := range fraudCols {
		if err := s.conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("migration 002 (%s): %w", q, err)
		}
	}

	// 003: app_logs table for centralized logging (frontend + backend)
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS app_logs (
			ts          DateTime64(3),
			level       LowCardinality(String),
			source      LowCardinality(String),
			user_id     UUID,
			session_id  String DEFAULT '',
			cat         LowCardinality(String),
			msg         String,
			ctx         String DEFAULT '',
			server_id   String DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (ts, level, user_id)
		TTL toDateTime(ts) + INTERVAL 14 DAY
	`); err != nil {
		return fmt.Errorf("migration 003: %w", err)
	}

	// 004a: observability columns. These are required — every INSERT now
	// targets all 16 columns, so a missing column would break ingest.
	obsCols := []string{
		"ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS trace_id    String DEFAULT '' CODEC(ZSTD(1))",
		"ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS request_id  String DEFAULT '' CODEC(ZSTD(1))",
		"ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS duration_ms UInt32 DEFAULT 0",
		"ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS stacktrace  String DEFAULT '' CODEC(ZSTD(3))",
		"ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS fingerprint String DEFAULT '' CODEC(ZSTD(1))",
		"ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS file        String DEFAULT ''",
		"ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS line        UInt32 DEFAULT 0",
	}
	for _, q := range obsCols {
		if err := s.conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("migration 004a (%s): %w", q, err)
		}
	}

	// 004b: bloom_filter indexes for trace_id/fingerprint lookup. These are
	// best-effort — `ADD INDEX IF NOT EXISTS` only landed in CH 23.3, so on
	// older builds we log and move on. Queries still work without them, just
	// scan more granules.
	obsIdx := []string{
		"ALTER TABLE app_logs ADD INDEX IF NOT EXISTS idx_trace_id   trace_id   TYPE bloom_filter GRANULARITY 4",
		"ALTER TABLE app_logs ADD INDEX IF NOT EXISTS idx_fingerprint fingerprint TYPE bloom_filter GRANULARITY 4",
	}
	for _, q := range obsIdx {
		if err := s.conn.Exec(ctx, q); err != nil {
			log.Printf("clickhouse: migration 004b skipped (%s): %v", q, err)
		}
	}

	// 005: error grouping. Hourly buckets per fingerprint via AggregatingMergeTree
	// fed by a materialized view. Best-effort — if the engine doesn't accept the
	// shape we log and move on; /admin/errors will degrade gracefully with the
	// table missing (handler returns empty rows).
	migration005 := []string{
		`CREATE TABLE IF NOT EXISTS app_errors_hourly (
			hour            DateTime,
			fingerprint     String,
			sample_msg      AggregateFunction(any, String),
			sample_stack    AggregateFunction(any, String),
			sample_file     AggregateFunction(any, String),
			sample_line     AggregateFunction(any, UInt32),
			sample_cat      AggregateFunction(any, String),
			errors          AggregateFunction(countIf, UInt8),
			warns           AggregateFunction(countIf, UInt8),
			users_affected  AggregateFunction(uniqCombined, UUID),
			last_seen       AggregateFunction(max, DateTime64(3))
		) ENGINE = AggregatingMergeTree()
		PARTITION BY toYYYYMM(hour)
		ORDER BY (hour, fingerprint)
		TTL toDateTime(hour) + INTERVAL 90 DAY`,

		`CREATE MATERIALIZED VIEW IF NOT EXISTS app_errors_hourly_mv TO app_errors_hourly AS
		SELECT
			toStartOfHour(ts) AS hour,
			fingerprint,
			anyState(msg)                 AS sample_msg,
			anyState(stacktrace)          AS sample_stack,
			anyState(file)                AS sample_file,
			anyState(line)                AS sample_line,
			anyState(cat)                 AS sample_cat,
			countIfState(level = 'error') AS errors,
			countIfState(level = 'warn')  AS warns,
			uniqCombinedState(user_id)    AS users_affected,
			maxState(ts)                  AS last_seen
		FROM app_logs
		WHERE level IN ('error', 'warn') AND fingerprint != ''
		GROUP BY hour, fingerprint`,
	}
	for i, q := range migration005 {
		if err := s.conn.Exec(ctx, q); err != nil {
			log.Printf("clickhouse: migration 005[%d] skipped: %v", i, err)
		}
	}

	// 006: distributed tracing spans. One row per span, parent_span_id links
	// children to the root for waterfall reconstruction in admin. Best-effort —
	// if the engine refuses, span ingest degrades to a no-op (sink logs the
	// error) but the rest of the app keeps running.
	// 007: release_sha column for correlating errors to a specific build.
	// Tolerant — older CH builds may reject `IF NOT EXISTS` on ADD COLUMN; we
	// log and continue. INSERT statements include the column anyway, so on
	// failure we'd surface a clearer error there if the column is genuinely
	// missing.
	if err := s.conn.Exec(ctx, `ALTER TABLE app_logs ADD COLUMN IF NOT EXISTS release_sha String DEFAULT '' CODEC(ZSTD(1))`); err != nil {
		log.Printf("clickhouse: migration 007 (release_sha) skipped: %v", err)
	}

	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS app_spans (
			trace_id        String CODEC(ZSTD(1)),
			span_id         String CODEC(ZSTD(1)),
			parent_span_id  String DEFAULT '' CODEC(ZSTD(1)),
			name            LowCardinality(String),
			service         LowCardinality(String) DEFAULT 'backend',
			started_at      DateTime64(3),
			duration_ms     UInt32 DEFAULT 0,
			status          Enum8('ok'=1, 'error'=2) DEFAULT 'ok',
			user_id         UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000'),
			attrs           String DEFAULT '' CODEC(ZSTD(3))
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(started_at)
		ORDER BY (trace_id, started_at)
		TTL toDateTime(started_at) + INTERVAL 14 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 006 skipped: %v", err)
	}

	// 008 (infra_logs, host_metrics) is gone: both tables were written by a
	// vector sidecar on the prod hosts, and that contour went to ARCHIVE with
	// task 55. Nothing wrote them and nothing read them; the numbers are not
	// reused so a future migration cannot be mistaken for one of them.

	// 009: synthetic_probes — uptime + packet-loss samples taken by
	// cmd/sfu-prober every ~15s from each SFU host against every other.
	// Best-effort: prober nodes write directly via the ingest endpoint, so
	// even if the table is missing /api/v1/internal/probes returns 503 and
	// no other subsystem breaks.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS synthetic_probes (
			ts              DateTime CODEC(ZSTD(1)),
			source_region   LowCardinality(String),
			target_region   LowCardinality(String),
			probe_type      Enum8('udp'=1, 'tcp'=2, 'tls'=3),
			rtt_ms          Float32 CODEC(ZSTD(1)),
			jitter_ms       Float32 CODEC(ZSTD(1)),
			packet_loss_pct Float32 CODEC(ZSTD(1)),
			success         UInt8,
			packets_sent    UInt16 DEFAULT 0,
			packets_recv    UInt16 DEFAULT 0
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (target_region, source_region, ts)
		TTL ts + INTERVAL 90 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 009 (synthetic_probes) skipped: %v", err)
	}

	// 010: webrtc_stats — per-client RTC.getStats() snapshots pushed every
	// ~10s by the frontend stats-monitor. Used by admin SFU dashboards to
	// surface real-user-experienced jitter / packet loss alongside the
	// synthetic probes (which only see the network path, not the codec or
	// the audio element pipeline).
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS webrtc_stats (
			ts                DateTime CODEC(ZSTD(1)),
			session_id        String CODEC(ZSTD(1)),
			user_id           UUID,
			node_region       LowCardinality(String),
			client_kind       LowCardinality(String),
			rtt_ms            Float32 CODEC(ZSTD(1)),
			jitter_ms         Float32 CODEC(ZSTD(1)),
			packet_loss_pct   Float32 CODEC(ZSTD(1)),
			packets_lost      UInt32 DEFAULT 0,
			packets_received  UInt32 DEFAULT 0,
			nack_count        UInt32 DEFAULT 0,
			audio_level       Float32 DEFAULT 0,
			bytes_per_sec     UInt32 DEFAULT 0
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (node_region, ts, user_id)
		TTL ts + INTERVAL 30 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 010 (webrtc_stats) skipped: %v", err)
	}

	// 011: csp_violations — Content-Security-Policy violation reports posted
	// by browsers to /api/csp-report. While CSP runs in Report-Only mode this
	// table is the audit signal for "what would I have blocked"; once enforce
	// is on it stays useful for noticing when a new dependency tries to load
	// from a host the policy doesn't list.
	//
	// `raw` keeps the full JSON in case a directive shows up that we haven't
	// extracted into a column yet. ZSTD on raw is worth it — typical reports
	// are 500-1500 bytes of mostly repeated text.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS csp_violations (
			ts                   DateTime64(3),
			ip                   String CODEC(ZSTD(1)),
			document_uri         String CODEC(ZSTD(1)),
			referrer             String CODEC(ZSTD(1)),
			violated_directive   LowCardinality(String),
			effective_directive  LowCardinality(String),
			blocked_uri          String CODEC(ZSTD(1)),
			source_file          String CODEC(ZSTD(1)),
			line_number          UInt32 DEFAULT 0,
			column_number        UInt32 DEFAULT 0,
			disposition          LowCardinality(String),
			raw                  String CODEC(ZSTD(3))
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (ts, violated_directive)
		TTL toDateTime(ts) + INTERVAL 30 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 011 (csp_violations) skipped: %v", err)
	}

	// ⚠️ ORPHANED 2026-09-07. The route that filled this table was removed with
	// the rest of the HTTP log ingest, and the Tauri launcher that produced the
	// events lives in ARCHIVE/. Nothing writes here and nothing reads it. The
	// table itself is left standing because dropping one is a data decision, not
	// a code cleanup — task 38 builds a new launcher and gets to say whether it
	// wants this shape back.
	//
	// 012 and 013 (launcher_crash_reports and its event_kind column) are gone
	// with the Electron/Tauri launcher that produced the rows. The Flutter
	// client has no watchdog, no rollback NSIS and no crash-report file, and
	// the ingest route they fed was removed with the rest of that surface.

	// 014: events — the product analytics table every admin Analytics view
	// reads and InsertEvents writes.
	//
	// It was missing from this runner, and the miss was invisible for a
	// reason worth recording: the schema used to be applied by hand, from
	// deploy/clickhouse/init.sql on the prod box. That file went to ARCHIVE
	// with the rest of the prod contour (task 55), and with it the only
	// thing that had ever created the table — so every insert and every
	// query started failing with «Unknown table expression identifier
	// 'events'», in a log nobody was reading because the admin API had no
	// process to run in.
	//
	// Schema is the prod one verbatim: it is the shape the rows in flight
	// already have, and inventing a better one here would only mean the
	// column list disagreeing with InsertEvents.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS events (
			user_id    UUID,
			session_id UUID,
			event_type LowCardinality(String),
			properties String,
			created_at DateTime64(3)
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMM(created_at)
		ORDER BY (event_type, user_id, created_at)
		TTL toDateTime(created_at) + INTERVAL 12 MONTH
	`); err != nil {
		log.Printf("clickhouse: migration 014 (events) skipped: %v", err)
	}

	// 015: health_probes — the 10s samples the health tracker takes against
	// Postgres/Redis/ClickHouse/S3/SFU, which the admin Health tab renders as
	// 24h sparklines per (target, kind).
	//
	// Same story as 014: written by InsertHealthProbes, read by the Health
	// tab, created by nothing. Ordered by (target, kind, ts) because every
	// query is «this target's last 24 hours».
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS health_probes (
			ts         DateTime CODEC(ZSTD(1)),
			target     LowCardinality(String),
			kind       LowCardinality(String),
			status     LowCardinality(String),
			latency_ms UInt32 DEFAULT 0,
			http_code  UInt16 DEFAULT 0,
			error      String DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (target, kind, ts)
		TTL ts + INTERVAL 30 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 015 (health_probes) skipped: %v", err)
	}

	// 016: entry_probes — Reality-relay liveness + cause-diagnosis samples
	// taken by cmd/entry-prober from an RU vantage against each entry relay
	// (plan 42 П6.6). Feeds the П6.7 censor-score dataset and the admin entry
	// dashboard's latest-verdict-per-relay. Best-effort like synthetic_probes:
	// if the table is missing the ingest endpoint returns 503 and nothing else
	// breaks. Kept 180 days — longer than the 90-day synthetic series because
	// this is the training set for the censor-score classifier, not an ops
	// dashboard that only cares about the recent window.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS entry_probes (
			ts             DateTime CODEC(ZSTD(1)),
			source_region  LowCardinality(String),
			relay_name     LowCardinality(String),
			raw_tcp        UInt8 DEFAULT 0,
			reality_ok     UInt8 DEFAULT 0,
			reality_rtt_ms Float32 CODEC(ZSTD(1)),
			reality_http   UInt16 DEFAULT 0,
			k1_bare_tls    UInt8 DEFAULT 0,
			k2_control     UInt8 DEFAULT 0,
			cover          LowCardinality(String) DEFAULT '',
			verdict        Enum8('unknown'=0, 'clean'=1, 'ip_banned'=2, 'fingerprint'=3, 'reality_wins'=4, 'network_down'=5),
			detail         String DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (relay_name, source_region, ts)
		TTL ts + INTERVAL 180 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 016 (entry_probes) skipped: %v", err)
	}

	// 017: entry_probes loses relay_address. The relay address is the secret
	// of plan 42 §9 — Postgres keeps it sealed under the server key — and a
	// plain copy in an analytics table undid that. The relay name says which
	// box (<box>-<n>), and the owner's console opens the address from the
	// sealed record when it needs it.
	if err := s.conn.Exec(ctx, `ALTER TABLE entry_probes DROP COLUMN IF EXISTS relay_address`); err != nil {
		log.Printf("clickhouse: migration 017 (entry_probes drop relay_address) skipped: %v", err)
	}

	// 018: entry_benches — one row per bench run of a relay build (plan 42
	// П6.14 Ф3): chat RTT, media throughput, the voice-like stream, breaks,
	// tagged with the build (edge_sha) and which side of the swap it measured.
	// The rollout decides from Postgres; this is the dataset beside
	// entry_probes, kept as long.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS entry_benches (
			ts               DateTime CODEC(ZSTD(1)),
			task_id          String,
			source_region    LowCardinality(String),
			relay_name       LowCardinality(String),
			box              LowCardinality(String),
			arch             LowCardinality(String),
			edge_sha         String,
			phase            LowCardinality(String),
			duration_s       Float32,
			chat_n           UInt32,
			chat_errors      UInt32,
			chat_rtt_p50_ms  Float32,
			chat_rtt_p95_ms  Float32,
			media_n          UInt32,
			media_errors     UInt32,
			down_mbps        Float32,
			up_mbps          Float32,
			bytes_down       UInt64,
			bytes_up         UInt64,
			voice_sent       UInt32,
			voice_lost       UInt32,
			voice_late       UInt32,
			voice_rtt_p50_ms Float32,
			voice_jitter_ms  Float32,
			voice_drops      UInt32,
			drops            UInt32,
			error            String DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMM(ts)
		ORDER BY (box, ts)
		TTL ts + INTERVAL 180 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 018 (entry_benches) skipped: %v", err)
	}

	// 019: entry_probes learns domain_blocked — an XHTTP relay (plan 42 П4в)
	// whose address answers while its domain is cut by SNI. Appending a value
	// to an Enum8 is a metadata-only change in ClickHouse.
	if err := s.conn.Exec(ctx, `
		ALTER TABLE entry_probes MODIFY COLUMN verdict
			Enum8('unknown'=0, 'clean'=1, 'ip_banned'=2, 'fingerprint'=3, 'reality_wins'=4, 'network_down'=5, 'domain_blocked'=6)
	`); err != nil {
		log.Printf("clickhouse: migration 019 (entry_probes domain_blocked) skipped: %v", err)
	}

	// 020: entry_reach — how clients' attempts to reach the service ended
	// (plan 42 П9.9). The doorway metric of the entry-points console: the
	// share that got through at once, the share the carrier race had to work
	// relays for, and the share that never got through at all.
	//
	// `account` is a keyed pseudonym, not an account id (internal/entry:
	// reachPseudonym): the console counts distinct people without the dataset
	// holding a list of them. Ordered by (ts, outcome) because every query
	// here is a window over time sliced by outcome; the relay is a filter on
	// top, not a leading key. Kept as long as the probes it is compared
	// against — a ninety-day chart needs more than ninety days of history.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS entry_reach (
			ts           DateTime CODEC(ZSTD(1)),
			account      FixedString(32) CODEC(ZSTD(1)),
			outcome      Enum8('direct'=1, 'relay'=2, 'failed'=3),
			relay_name   LowCardinality(String) DEFAULT '',
			relays_tried UInt8 DEFAULT 0,
			connect_ms   UInt32 DEFAULT 0,
			rtt_ms       UInt32 DEFAULT 0
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (ts, outcome)
		TTL ts + INTERVAL 180 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 020 (entry_reach) skipped: %v", err)
	}

	// 021: entry_relay_stats — what a relay says about itself every half
	// minute (plan 42 П9.3). The third vantage of the console: the prober
	// asks whether a relay can be reached from Russia, the client whether a
	// person got through, and this one what the machine is actually doing.
	//
	// Counters are totals since the edge process started, never deltas: a
	// lost pulse then costs nothing, because the next one still carries the
	// whole, and a restart is visible as a counter going down rather than
	// hidden as a quiet gap. A reader differences consecutive rows and skips
	// the pair where uptime_s fell.
	//
	// Nothing here identifies anyone. Bytes and a connection count say how
	// loaded a box is and nothing about who loaded it — the invariant of plan
	// 42 §0.1, which is why an edge keeps no session list to report from.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS entry_relay_stats (
			ts         DateTime CODEC(ZSTD(1)),
			relay_name LowCardinality(String),
			proto      LowCardinality(String),
			uptime_s   UInt32 DEFAULT 0,
			bytes_up   UInt64 DEFAULT 0,
			bytes_down UInt64 DEFAULT 0,
			conns      UInt64 DEFAULT 0,
			cpu_pct    Float32 DEFAULT 0,
			mem_mb     UInt32 DEFAULT 0,
			version    LowCardinality(String) DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (relay_name, ts)
		TTL ts + INTERVAL 180 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 021 (entry_relay_stats) skipped: %v", err)
	}

	// 022: entry_probes keeps the voice door as its own pair of numbers (plan
	// 42 П9.7). A stream relay that also carries calls is probed with a burst
	// of datagrams through its Reality session, and the result used to be
	// folded into the verdict — a `clean` row became `unknown` when the door
	// was shut. One column then carried two independent claims, and the word
	// it carried is the one everything downstream reads as "no evidence": the
	// detector stopped seeing healthy boxes and every repair failed its own
	// admission probe. The door is recorded here instead, where it can be
	// asked about without asking about the stream.
	// Written out twice rather than looped over a slice of names: the query
	// text handed to Exec stays a compile-time constant, which is the
	// project's rule for SQL and what tools/sqlguard enforces.
	if err := s.conn.Exec(ctx,
		`ALTER TABLE entry_probes ADD COLUMN IF NOT EXISTS xudp_got UInt16 DEFAULT 0`); err != nil {
		log.Printf("clickhouse: migration 022 (entry_probes xudp_got) skipped: %v", err)
	}
	if err := s.conn.Exec(ctx,
		`ALTER TABLE entry_probes ADD COLUMN IF NOT EXISTS xudp_sent UInt16 DEFAULT 0`); err != nil {
		log.Printf("clickhouse: migration 022 (entry_probes xudp_sent) skipped: %v", err)
	}

	// 023: entry_user_traffic — what each account's message socket carried
	// and through which box it came (plan 42 П9.9). The per-person half of
	// the console's traffic: the pulse (021) says how loaded a box is and
	// nothing about who, this says how much one person moved and nothing
	// about what.
	//
	// `account` is the same keyed pseudonym as entry_reach's, so the two
	// count the same people; `box` is the hub's resolution of the socket's
	// peer, empty for a direct socket, and the address itself is not kept.
	// Rows are deltas the gateway flushes itself, so a sum over a window is
	// the traffic of that window. Kept as long as entry_probes.
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS entry_user_traffic (
			ts         DateTime CODEC(ZSTD(1)),
			account    FixedString(32) CODEC(ZSTD(1)),
			box        LowCardinality(String) DEFAULT '',
			bytes_up   UInt64 DEFAULT 0,
			bytes_down UInt64 DEFAULT 0
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMMDD(ts)
		ORDER BY (ts, account)
		TTL ts + INTERVAL 180 DAY
	`); err != nil {
		log.Printf("clickhouse: migration 023 (entry_user_traffic) skipped: %v", err)
	}

	return nil
}

// InsertEntryRelayStats writes a batch of relay pulses (plan 42 П9.3). Async
// like the probes: every relay in the pool reports twice a minute, so the hub
// must not pay an fsync per box, and a lost pulse costs nothing — the next one
// carries the same totals.
func (s *Store) InsertEntryRelayStats(ctx context.Context, records []EntryRelayStatRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO entry_relay_stats (ts, relay_name, proto, uptime_s, bytes_up, bytes_down, conns, cpu_pct, mem_mb, version)")
	if err != nil {
		return fmt.Errorf("prepare entry_relay_stats batch: %w", err)
	}
	for _, r := range records {
		if err := batch.Append(r.Timestamp, r.RelayName, r.Proto, r.UptimeS,
			r.BytesUp, r.BytesDown, r.Conns, r.CPUPct, r.MemMB, r.Version); err != nil {
			return fmt.Errorf("append relay stat: %w", err)
		}
	}
	return batch.Send()
}

// InsertEntryUserTraffic writes a batch of per-socket byte counts (plan 42
// П9.9). Async like the reach rows: every signed-in socket flushes every few
// minutes, and a lost row costs a fraction of one person's figure.
func (s *Store) InsertEntryUserTraffic(ctx context.Context, records []EntryUserTrafficRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO entry_user_traffic (ts, account, box, bytes_up, bytes_down)")
	if err != nil {
		return fmt.Errorf("prepare entry_user_traffic batch: %w", err)
	}
	for _, r := range records {
		if err := batch.Append(r.Timestamp, r.Account, r.Box, r.BytesUp, r.BytesDown); err != nil {
			return fmt.Errorf("append user traffic: %w", err)
		}
	}
	return batch.Send()
}

// InsertEntryReach writes a batch of client reach outcomes (plan 42 П9.9).
// Async like the probes: every authenticated connect may carry a few rows, so
// the hub must not pay an fsync per client, and a lost row costs a fraction of
// a percent in a chart rather than a decision.
func (s *Store) InsertEntryReach(ctx context.Context, records []EntryReachRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO entry_reach (ts, account, outcome, relay_name, relays_tried, connect_ms, rtt_ms)")
	if err != nil {
		return fmt.Errorf("prepare entry_reach batch: %w", err)
	}
	for _, r := range records {
		if !validReachOutcome(r.Outcome) {
			continue
		}
		if err := batch.Append(r.Timestamp, r.Account, r.Outcome, r.RelayName,
			r.RelaysTried, r.ConnectMs, r.RTTMs); err != nil {
			return fmt.Errorf("append entry reach: %w", err)
		}
	}
	return batch.Send()
}

// validReachOutcome guards the Enum8: an unknown value fails the whole batch
// at insert time, so a row that does not belong is dropped before it can cost
// the rows beside it.
func validReachOutcome(v string) bool {
	switch v {
	case "direct", "relay", "failed":
		return true
	}
	return false
}

// InsertProbes writes a batch of synthetic-probe samples to ClickHouse.
// Same async-insert pattern as InsertLogs — the prober pushes small batches
// every 15s and we coalesce server-side instead of paying fsync per row.
func (s *Store) InsertProbes(ctx context.Context, records []ProbeRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO synthetic_probes (ts, source_region, target_region, probe_type, rtt_ms, jitter_ms, packet_loss_pct, success, packets_sent, packets_recv)")
	if err != nil {
		return fmt.Errorf("prepare probes batch: %w", err)
	}
	for _, r := range records {
		probeType := r.ProbeType
		if probeType != "udp" && probeType != "tcp" && probeType != "tls" {
			probeType = "udp"
		}
		if err := batch.Append(r.Timestamp, r.SourceRegion, r.TargetRegion, probeType,
			r.RTTMs, r.JitterMs, r.PacketLossPct, r.Success, r.PacketsSent, r.PacketsRecv); err != nil {
			return fmt.Errorf("append probe: %w", err)
		}
	}
	return batch.Send()
}

// validEntryVerdict is the set accepted by the entry_probes Enum8; anything
// else is coerced to "unknown" rather than failing the whole batch.
func validEntryVerdict(v string) bool {
	switch v {
	case "clean", "ip_banned", "fingerprint", "reality_wins", "network_down", "unknown", "domain_blocked":
		return true
	default:
		return false
	}
}

// InsertEntryProbes writes a batch of Reality-relay liveness samples to
// ClickHouse. Same async-insert pattern as InsertProbes — cmd/entry-prober
// pushes one small batch per round and CH coalesces server-side.
func (s *Store) InsertEntryProbes(ctx context.Context, records []EntryProbeRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO entry_probes (ts, source_region, relay_name, raw_tcp, reality_ok, reality_rtt_ms, reality_http, k1_bare_tls, k2_control, cover, verdict, detail, xudp_got, xudp_sent)")
	if err != nil {
		return fmt.Errorf("prepare entry_probes batch: %w", err)
	}
	for _, r := range records {
		verdict := r.Verdict
		if !validEntryVerdict(verdict) {
			verdict = "unknown"
		}
		detail := r.Detail
		if len(detail) > 256 {
			detail = detail[:256]
		}
		if err := batch.Append(r.Timestamp, r.SourceRegion, r.RelayName,
			r.RawTCP, r.RealityOK, r.RealityRTTMs, r.RealityHTTP, r.K1BareTLS, r.K2Control,
			r.Cover, verdict, detail, r.XUDPGot, r.XUDPSent); err != nil {
			return fmt.Errorf("append entry probe: %w", err)
		}
	}
	return batch.Send()
}

// InsertEntryBench writes one bench run. Synchronous (wait_for_async_insert):
// a run is minutes of traffic and one row, so there is nothing to coalesce,
// and a lost row should be an error in the hub's log, not silence.
func (s *Store) InsertEntryBench(ctx context.Context, r EntryBenchRecord) error {
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO entry_benches (ts, task_id, source_region, relay_name,
		box, arch, edge_sha, phase, duration_s, chat_n, chat_errors, chat_rtt_p50_ms, chat_rtt_p95_ms, media_n,
		media_errors, down_mbps, up_mbps, bytes_down, bytes_up, voice_sent, voice_lost, voice_late,
		voice_rtt_p50_ms, voice_jitter_ms, voice_drops, drops, error)`)
	if err != nil {
		return fmt.Errorf("prepare entry_benches: %w", err)
	}
	errText := r.Error
	if len(errText) > 256 {
		errText = errText[:256]
	}
	if err := batch.Append(r.Timestamp, r.TaskID, r.SourceRegion, r.RelayName, r.Box, r.Arch, r.EdgeSHA,
		r.Phase, r.DurationS, r.ChatN, r.ChatErrors, r.ChatRTTP50Ms, r.ChatRTTP95Ms, r.MediaN, r.MediaErrors,
		r.DownMbps, r.UpMbps, r.BytesDown, r.BytesUp, r.VoiceSent, r.VoiceLost, r.VoiceLate, r.VoiceRTTP50,
		r.VoiceJitter, r.VoiceDrops, r.Drops, errText); err != nil {
		return fmt.Errorf("append entry bench: %w", err)
	}
	return batch.Send()
}

// FailingRelays returns when a routine probe last saw each relay fail since
// `since` (plan 42 П6.14: the canary must not be a box that is already sick).
// Admission probes are not routine, and network_down blames the vantage, not
// the relay, so neither counts.
//
// Neither does `unknown`, for the reason DiagnoseBox gives in so many words:
// it is the absence of evidence, not evidence of absence. Counting it here
// while the detector refuses to count it there let one word mean two opposite
// things, and the stand showed what that costs on 24.10 — the box answered
// `unknown` every minute because its old edge drops the XUDP datagrams the
// hub had already begun to probe for, so it was held sick for 24 hours, so
// the canary would not take it, so the build that opens that very door could
// never roll out. A relay nobody could judge is not a relay known to be
// broken.
//
// Nor does a row dated in the future (audit 42-autopilot К1): its time is the
// prober's word, and one stamped ahead would hold the relay "sick" until the
// table's TTL. The ingest refuses them now; the bound keeps any that got in
// before from counting.
//
// Only the rows of `slots` count (audit 42 recheck НН3): the caller's list of
// active observation slots, the same allowlist the detector reads under.
func (s *Store) FailingRelays(ctx context.Context, since time.Time, slots []string) (map[string]time.Time, error) {
	if len(slots) == 0 {
		return map[string]time.Time{}, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT relay_name, max(ts) FROM entry_probes
		WHERE ts >= ? AND ts <= now() + INTERVAL 2 MINUTE AND detail != 'admission'
		  AND has(?, source_region)
		  AND verdict NOT IN ('clean', 'reality_wins', 'network_down', 'unknown')
		GROUP BY relay_name`, since, slots)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var name string
		var last time.Time
		if err := rows.Scan(&name, &last); err != nil {
			return nil, err
		}
		out[name] = last
	}
	return out, rows.Err()
}

// RelayRTT is the median tunnel RTT of a relay's successful probes in
// [from, to) and their count (plan 42 П6.14: is the path to a lone canary as
// steady as usual?). Routine and admission probes both count: after a swap
// the admission probe is the only sample there is. Only the rows of `slots`,
// as FailingRelays.
func (s *Store) RelayRTT(ctx context.Context, relay string, from, to time.Time, slots []string) (float64, int, error) {
	if len(slots) == 0 {
		return 0, 0, nil
	}
	var median float64
	var n uint64
	row := s.conn.QueryRow(ctx, `
		SELECT toFloat64(quantile(0.5)(reality_rtt_ms)), count() FROM entry_probes
		WHERE relay_name = ? AND ts >= ? AND ts < ? AND reality_ok = 1
		  AND has(?, source_region)`, relay, from, to, slots)
	if err := row.Scan(&median, &n); err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, nil
	}
	return median, int(n), nil
}

// InsertHealthProbes writes a batch of health-check samples to ClickHouse.
// Same async-insert pattern as InsertProbes — a small batch every 5s, which
// CH coalesces on its end.
func (s *Store) InsertHealthProbes(ctx context.Context, records []HealthProbeRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO health_probes (ts, target, kind, status, latency_ms, http_code, error)")
	if err != nil {
		return fmt.Errorf("prepare health_probes batch: %w", err)
	}
	for _, r := range records {
		kind := r.Kind
		if kind != "internal" && kind != "external" {
			kind = "internal"
		}
		status := r.Status
		switch status {
		case "ok", "error", "degraded", "timeout", "unknown":
		default:
			status = "unknown"
		}
		errMsg := r.Error
		if len(errMsg) > 256 {
			errMsg = errMsg[:256]
		}
		if err := batch.Append(r.Timestamp, r.Target, kind, status, r.LatencyMs, r.HTTPCode, errMsg); err != nil {
			return fmt.Errorf("append health probe: %w", err)
		}
	}
	return batch.Send()
}

// InsertCSPViolation writes a single CSP report. Single-row entry point used
// by middleware.CSPReportHandler — async_insert lets CH coalesce on its end
// so we don't need a batching layer here.
func (s *Store) InsertCSPViolation(ctx context.Context, v CSPViolationRecord) error {
	return s.InsertCSPViolations(ctx, []CSPViolationRecord{v})
}

// InsertCSPViolations writes a batch of CSP violation reports to ClickHouse.
// Same async_insert pattern as InsertProbes — reports trickle in one at a
// time from the middleware handler, CH coalesces server-side.
func (s *Store) InsertCSPViolations(ctx context.Context, records []CSPViolationRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO csp_violations (ts, ip, document_uri, referrer, violated_directive, effective_directive, blocked_uri, source_file, line_number, column_number, disposition, raw)")
	if err != nil {
		return fmt.Errorf("prepare csp batch: %w", err)
	}
	for _, r := range records {
		if err := batch.Append(r.Timestamp, r.IP, r.DocumentURI, r.Referrer,
			r.ViolatedDirective, r.EffectiveDirective, r.BlockedURI, r.SourceFile,
			r.LineNumber, r.ColumnNumber, r.Disposition, r.Raw); err != nil {
			return fmt.Errorf("append csp violation: %w", err)
		}
	}
	return batch.Send()
}

// InsertWebRTCStats writes a batch of client-side getStats() samples to
// ClickHouse. Frontend pushes every 10s; we flush them to CH async so the
// admin dashboards have ~1-min freshness without back-pressuring the client.
func (s *Store) InsertWebRTCStats(ctx context.Context, records []WebRTCStatRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO webrtc_stats (ts, session_id, user_id, node_region, client_kind, rtt_ms, jitter_ms, packet_loss_pct, packets_lost, packets_received, nack_count, audio_level, bytes_per_sec)")
	if err != nil {
		return fmt.Errorf("prepare webrtc_stats batch: %w", err)
	}
	for _, r := range records {
		kind := r.ClientKind
		if kind != "browser" && kind != "desktop" {
			kind = "browser"
		}
		if err := batch.Append(r.Timestamp, r.SessionID, r.UserID, r.NodeRegion, kind,
			r.RTTMs, r.JitterMs, r.PacketLossPct, r.PacketsLost, r.PacketsReceived,
			r.NackCount, r.AudioLevel, r.BytesPerSec); err != nil {
			return fmt.Errorf("append webrtc_stat: %w", err)
		}
	}
	return batch.Send()
}

// InsertSpans writes a batch of trace spans to ClickHouse.
func (s *Store) InsertSpans(ctx context.Context, records []SpanRecord) error {
	if len(records) == 0 {
		return nil
	}
	// Same async-insert pattern as InsertLogs — coalesce small batches
	// server-side instead of paying fsync per span.
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO app_spans (trace_id, span_id, parent_span_id, name, service, started_at, duration_ms, status, user_id, attrs)")
	if err != nil {
		return fmt.Errorf("prepare spans batch: %w", err)
	}

	for _, r := range records {
		status := r.Status
		if status != "error" {
			status = "ok"
		}
		service := r.Service
		if service == "" {
			service = "backend"
		}
		if err := batch.Append(r.TraceID, r.SpanID, r.ParentSpanID, r.Name, service, r.StartedAt, r.DurationMs, status, r.UserID, r.Attrs); err != nil {
			return fmt.Errorf("append span: %w", err)
		}
	}

	return batch.Send()
}

// InsertLogs writes a batch of log records to ClickHouse.
func (s *Store) InsertLogs(ctx context.Context, records []LogRecord) error {
	if len(records) == 0 {
		return nil
	}
	// Server-side async insert: CH buffers rows in memory and flushes by 1MB /
	// 200ms (CH 24+ defaults). Client returns once the row is queued, so fsync
	// latency no longer back-pressures the ingest path.
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 0,
	}))
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO app_logs (ts, level, source, user_id, session_id, cat, msg, ctx, server_id, trace_id, request_id, duration_ms, stacktrace, fingerprint, file, line, release_sha)")
	if err != nil {
		return fmt.Errorf("prepare logs batch: %w", err)
	}

	for _, r := range records {
		if err := batch.Append(r.Timestamp, r.Level, r.Source, r.UserID, r.SessionID, r.Category, r.Message, r.Context, r.ServerID, r.TraceID, r.RequestID, r.DurationMs, r.Stacktrace, r.Fingerprint, r.File, r.Line, r.ReleaseSHA); err != nil {
			return fmt.Errorf("append log: %w", err)
		}
	}

	return batch.Send()
}

// InsertBatch writes a batch of events to ClickHouse.
func (s *Store) InsertBatch(ctx context.Context, events []Event) error {
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO events (user_id, session_id, event_type, properties, created_at)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, e := range events {
		if err := batch.Append(e.UserID, e.SessionID, e.EventType, e.Properties, e.CreatedAt); err != nil {
			return fmt.Errorf("append event: %w", err)
		}
	}

	return batch.Send()
}
