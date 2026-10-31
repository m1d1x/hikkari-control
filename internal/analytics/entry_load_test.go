package analytics

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// liveStore is a Store on a scratch database beside the one
// NEKKO_TEST_CLICKHOUSE names (the DSN the other ClickHouse tests take, e.g.
// clickhouse://nekko:nekko@localhost:9000/nekko_analytics), migrated and
// dropped after the test, so the shared dataset is never written. Skipped
// without it: the SQL here is only proven against the real engine.
func liveStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("NEKKO_TEST_CLICKHOUSE")
	if dsn == "" {
		t.Skip("NEKKO_TEST_CLICKHOUSE not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("NEKKO_TEST_CLICKHOUSE: %v", err)
	}
	at := func(db string) string {
		v := *u
		v.Path = "/" + db
		return v.String()
	}
	ctx := context.Background()
	admin, err := NewStore(at("default"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	db := fmt.Sprintf("nekko_test_load_%d", time.Now().UnixNano())
	if err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)
		_ = admin.Close()
	})
	s, err := NewStore(at(db))
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RunMigrations(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// awaitRows waits until the table holds n rows: the inserts are async, and
// the condition, not a guessed delay, is what the reads need.
func awaitRows(t *testing.T, s *Store, table string, n uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var got uint64
		if err := s.QueryRow(context.Background(), &got, "SELECT count() FROM "+table); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s holds %d rows, want %d", table, got, n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The pulses' counters run since the edge started: a group's rate is the sum
// of its relays' rates in its busier direction, a restart is neither a
// negative delta nor a burst, a lost pulse leaves the next one carrying two
// intervals of bytes at the same rate, not double, and a restart in the middle
// of a long silence — uptime and counters both past their old values again —
// is read as the restart it is.
func TestEntryLoad_RatesSurviveRestartsAndLostPulses(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	const mb = 1_000_000
	var recs []EntryRelayStatRecord
	pulse := func(relay string, at time.Time, uptime, up, down uint64, cpu float32) {
		recs = append(recs, EntryRelayStatRecord{
			Timestamp: at, RelayName: relay, Proto: "vless-reality-vision",
			UptimeS: uint32(uptime), BytesUp: up, BytesDown: down, CPUPct: cpu,
		})
	}
	for i := uint64(0); i <= 40; i++ { // twenty minutes, every 30 s
		at := start.Add(time.Duration(i) * 30 * time.Second)
		// A: one relay sends 1 MB/s down, the other 2 MB/s up. The busier
		// direction is 2 MB/s; the sum, 3, would be a link half as roomy.
		pulse("a-1", at, 1000+i*30, 0, i*30*1*mb, 20)
		pulse("a-2", at, 1000+i*30, i*30*2*mb, 0, 40)
		// B carries 1 MB/s, restarts at pulse 20 (its counters start over
		// from what it carried in the 30 s since) and loses pulse 30.
		switch {
		case i == 30:
		case i < 20:
			pulse("b-1", at, 5000+i*30, 0, i*30*mb, 10)
		default:
			up := (i - 19) * 30
			pulse("b-1", at, up, 0, up*mb, 10)
		}
	}
	// C carries 1 MB/s for ten minutes, falls silent for thirty, and comes
	// back as a process started fifteen minutes ago: uptime 900 > 700 and
	// 900 MB > 600 MB, so only the clock tells the restart. The pulse that
	// ends the silence covers fifteen unwatched minutes and is no reading.
	for i := uint64(0); i <= 20; i++ {
		pulse("c-1", start.Add(time.Duration(i)*30*time.Second), 100+i*30, 0, i*30*mb, 10)
	}
	for j := uint64(0); j <= 10; j++ {
		up := 900 + j*30
		pulse("c-1", start.Add(40*time.Minute+time.Duration(j)*30*time.Second), up, 0, up*mb, 10)
	}
	pulse("other", start, 10, 0, 10*mb, 99) // not in any group, not read
	if err := s.InsertEntryRelayStats(ctx, recs); err != nil {
		t.Fatalf("insert: %v", err)
	}
	awaitRows(t, s, "entry_relay_stats", uint64(len(recs)))

	rows, err := s.EntryLoad(ctx, start, 5*time.Minute, map[string]string{"a-1": "A", "a-2": "A", "b-1": "B", "c-1": "C"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := map[string]float64{"A": 16, "B": 8, "C": 8}   // Mbit/s, the busier direction
	volume := map[string]float64{"A": 24, "B": 8, "C": 8} // Mbit/s, both, as billed
	cpu := map[string]float64{"A": 30, "B": 10, "C": 10}
	seen := map[string]int{}
	for _, r := range rows {
		w, ok := want[r.Key]
		if !ok {
			t.Fatalf("unexpected group %q", r.Key)
		}
		seen[r.Key]++
		if math.Abs(r.Mbps-w) > 0.01 {
			t.Errorf("%s at %s: %.3f Mbit/s, want %.0f", r.Key, r.Bucket.Format("15:04"), r.Mbps, w)
		}
		if math.Abs(r.VolumeMbps-volume[r.Key]) > 0.01 {
			t.Errorf("%s at %s: volume %.3f Mbit/s, want %.0f", r.Key, r.Bucket.Format("15:04"), r.VolumeMbps, volume[r.Key])
		}
		if math.Abs(r.CPUPct-cpu[r.Key]) > 0.01 {
			t.Errorf("%s at %s: cpu %.2f, want %.0f", r.Key, r.Bucket.Format("15:04"), r.CPUPct, cpu[r.Key])
		}
	}
	// C: 0, 5, 10 (one pulse), then 40 and 45; nothing in the silence.
	if seen["A"] != 5 || seen["B"] != 5 || seen["C"] != 5 {
		t.Fatalf("buckets per group: %v, want 5 each", seen)
	}

	// The totals the console sums keep every byte of C: 600 MB before the
	// silence and all 1200 the restarted process carried, not the 600 that
	// subtracting the old counter would leave.
	var total uint64
	if err := s.QueryRow(ctx, &total,
		`SELECT sum(d_down) FROM (`+EntryRelayDeltas+`) WHERE relay_name = 'c-1'`, start); err != nil {
		t.Fatalf("totals: %v", err)
	}
	if total != 1800*mb {
		t.Fatalf("c-1 carried %d MB by the deltas, want 1800", total/mb)
	}
}

// RTT is read through relays only, per group, with the count it stands on;
// a bench row that failed is not a peak.
func TestEntryReachRTTAndBenchPeaks(t *testing.T) {
	s := liveStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	var reach []EntryReachRecord
	for i := 1; i <= 20; i++ {
		reach = append(reach, EntryReachRecord{
			Timestamp: at.Add(time.Duration(i) * time.Second), Account: fmt.Sprintf("%032d", i),
			Outcome: "relay", RelayName: "a-1", RTTMs: uint32(10 * i),
		})
	}
	reach = append(reach,
		EntryReachRecord{Timestamp: at, Account: fmt.Sprintf("%032d", 99), Outcome: "direct", RTTMs: 5000},
		EntryReachRecord{Timestamp: at, Account: fmt.Sprintf("%032d", 98), Outcome: "relay", RelayName: "zz", RTTMs: 5000},
	)
	if err := s.InsertEntryReach(ctx, reach); err != nil {
		t.Fatalf("insert reach: %v", err)
	}
	awaitRows(t, s, "entry_reach", uint64(len(reach)))
	rows, err := s.EntryReachRTT(ctx, at.Add(-time.Minute), 5*time.Minute, map[string]string{"a-1": "box-a"})
	if err != nil {
		t.Fatalf("rtt: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "box-a" || rows[0].N != 20 || rows[0].P95Ms < 180 || rows[0].P95Ms > 200 {
		t.Fatalf("rtt rows: %+v", rows)
	}

	for _, b := range []EntryBenchRecord{
		{Timestamp: at, TaskID: "1", Box: "box-a", DownMbps: 80},
		{Timestamp: at, TaskID: "2", Box: "box-a", DownMbps: 120},
		{Timestamp: at, TaskID: "3", Box: "box-a", DownMbps: 900, Error: "timeout"},
	} {
		if err := s.InsertEntryBench(ctx, b); err != nil {
			t.Fatalf("insert bench: %v", err)
		}
	}
	awaitRows(t, s, "entry_benches", 3)
	peaks, err := s.EntryBenchPeaks(ctx, at.Add(-time.Minute))
	if err != nil {
		t.Fatalf("bench peaks: %v", err)
	}
	if len(peaks) != 1 || peaks["box-a"] != 120 {
		t.Fatalf("bench peaks: %v", peaks)
	}
}

// The analytics database holds the fleet's datasets and the clients'
// telemetry — nothing carried over from the messenger.
func TestFreshAnalyticsContainsOnlyFleetTables(t *testing.T) {
	s := liveStore(t)
	rows, err := s.Query(context.Background(), `SELECT name FROM system.tables WHERE database=currentDatabase()`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		count++
		if !strings.HasPrefix(name, "entry_") && !strings.HasPrefix(name, "telemetry_") {
			t.Errorf("unexpected dataset: %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 5+len(telemetrySchema) {
		t.Errorf("datasets=%d, want %d", count, 5+len(telemetrySchema))
	}
}
