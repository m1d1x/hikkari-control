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

// RunMigrations creates only the fleet datasets. Every DDL failure stops
// startup; a partially initialized detector must never look operational.
func (s *Store) RunMigrations(ctx context.Context) error {
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
		return fmt.Errorf("fleet analytics schema 1: %w", err)
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE entry_probes DROP COLUMN IF EXISTS relay_address`); err != nil {
		return fmt.Errorf("fleet analytics schema 2: %w", err)
	}
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
		return fmt.Errorf("fleet analytics schema 3: %w", err)
	}
	if err := s.conn.Exec(ctx, `
		ALTER TABLE entry_probes MODIFY COLUMN verdict
			Enum8('unknown'=0, 'clean'=1, 'ip_banned'=2, 'fingerprint'=3, 'reality_wins'=4, 'network_down'=5, 'domain_blocked'=6)
	`); err != nil {
		return fmt.Errorf("fleet analytics schema 4: %w", err)
	}
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
		return fmt.Errorf("fleet analytics schema 5: %w", err)
	}
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
		return fmt.Errorf("fleet analytics schema 6: %w", err)
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE entry_probes ADD COLUMN IF NOT EXISTS xudp_got UInt16 DEFAULT 0`); err != nil {
		return fmt.Errorf("fleet analytics schema 7: %w", err)
	}
	if err := s.conn.Exec(ctx, `ALTER TABLE entry_probes ADD COLUMN IF NOT EXISTS xudp_sent UInt16 DEFAULT 0`); err != nil {
		return fmt.Errorf("fleet analytics schema 8: %w", err)
	}
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
		return fmt.Errorf("fleet analytics schema 9: %w", err)
	}
	return s.migrateTelemetry(ctx)
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
