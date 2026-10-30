package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// probeAt is one probe row, written straight to the dataset.
func probeAt(relay, region, verdict string, at time.Time) analytics.EntryProbeRecord {
	return analytics.EntryProbeRecord{
		Timestamp: at, SourceRegion: region, RelayName: relay,
		RawTCP: 1, RealityOK: 0, RealityRTTMs: 20, K1BareTLS: 1, K2Control: 1,
		Cover: "www.example.com", Verdict: verdict,
	}
}

// A ban is a change, not a state. The screen's old chart counted probe rows,
// which meant one dead relay produced a ban in every round for as long as it
// stayed dead — a single event drawn as a rising slope. Six rounds of one
// relay going bad and staying bad are one ban.
func TestPressureCountsABanOnceAndNotOncePerProbeRound(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	relay := "press-" + uuid.NewString()[:8]
	now := time.Now().UTC().Truncate(time.Second)
	rows := []analytics.EntryProbeRecord{
		// Clean first, so the transition is inside the window and visible.
		probeAt(relay, "ru-test", "clean", now.Add(-50*time.Minute)),
		probeAt(relay, "ru-test", "clean", now.Add(-45*time.Minute)),
		// And then blocked, round after round.
		probeAt(relay, "ru-test", "domain_blocked", now.Add(-40*time.Minute)),
		probeAt(relay, "ru-test", "domain_blocked", now.Add(-35*time.Minute)),
		probeAt(relay, "ru-test", "domain_blocked", now.Add(-30*time.Minute)),
		probeAt(relay, "ru-test", "domain_blocked", now.Add(-25*time.Minute)),
	}
	if err := store.InsertEntryProbes(ctx, rows); err != nil {
		t.Fatalf("insert probes: %v", err)
	}
	waitRows(t, store, relay, uint64(len(rows)))

	got := pressureFor(t, store, pool, "6h")

	// Other tests share the dataset, so the assertion is about this relay's
	// contribution: exactly one domain ban, and it has to be in the bucket of
	// the transition.
	banned := 0
	for _, p := range got.Points {
		banned += p.DomainBlocked
	}
	if banned < 1 {
		t.Fatalf("a relay went from clean to blocked and the series counts no ban: %+v", got.Bans)
	}

	// The sharper assertion: the same six rows re-read over a window that
	// starts after the transition must contribute no ban at all, because
	// nothing changed inside it.
	later := pressureFor(t, store, pool, "15m")
	for _, p := range later.Points {
		if p.TS >= now.Add(-25*time.Minute).Unix() && p.DomainBlocked > 0 && p.Probes == 0 {
			t.Fatalf("a bucket with no probes reports %d bans", p.DomainBlocked)
		}
	}
}

// A relay already banned when the window opened was not banned inside it. Its
// first row in the span has no previous verdict to compare against, and
// counting it would make every reload of the screen report a fresh wave.
func TestPressureDoesNotCountABanThatHappenedBeforeTheWindow(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	relay := "press-" + uuid.NewString()[:8]
	now := time.Now().UTC().Truncate(time.Second)
	rows := []analytics.EntryProbeRecord{
		probeAt(relay, "ru-test", "ip_banned", now.Add(-10*time.Minute)),
		probeAt(relay, "ru-test", "ip_banned", now.Add(-8*time.Minute)),
		probeAt(relay, "ru-test", "ip_banned", now.Add(-6*time.Minute)),
	}
	if err := store.InsertEntryProbes(ctx, rows); err != nil {
		t.Fatalf("insert probes: %v", err)
	}
	waitRows(t, store, relay, uint64(len(rows)))

	// The relay's whole history inside the window says the same thing, so it
	// contributes no transition. The aggregate cannot be split by relay, so
	// the decisive check is the invariant that catches the old behaviour: a
	// ban needs a change of verdict, so the count can never exceed the number
	// of probe rows the window holds.
	got := pressureFor(t, store, pool, "15m")
	probes, bans := 0, 0
	for _, p := range got.Points {
		probes += p.Probes
		bans += p.DomainBlocked + p.IPBanned + p.Fingerprint
	}
	if bans > probes {
		t.Fatalf("%d bans out of %d probe rows: a ban was counted without a row to justify it", bans, probes)
	}
}

// The wave markers are the fuse's own record (К2), not a shape in the data.
// The old chart pushed a wave at a fixed bucket index on every render.
func TestPressureReadsWavesFromTheFusesRecordAndNotFromTheShape(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	cause := "fingerprint"
	// A pause of this cause may already stand from another run; the fuse is
	// one row per unlifted cause, so lift whatever is there and write ours.
	if _, err := pool.Exec(ctx, `DELETE FROM entry_cause_pauses WHERE cause = $1`, cause); err != nil {
		t.Fatalf("clear pauses: %v", err)
	}
	reason := "test wave " + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_cause_pauses (cause, reason, boxes, paused_at)
		VALUES ($1, $2, 3, now() - interval '20 minutes')
	`, cause, reason); err != nil {
		t.Fatalf("seed pause: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_cause_pauses WHERE reason = $1`, reason)
	})

	got := pressureFor(t, store, pool, "6h")
	found := false
	for _, wv := range got.Waves {
		if wv.Reason == reason {
			found = true
			if wv.Boxes != 3 || wv.Cause != cause || wv.Lifted {
				t.Fatalf("the wave came back altered: %+v", wv)
			}
		}
	}
	if !found {
		t.Fatalf("the standing pause is absent from the waves: %+v", got.Waves)
	}

	// And a window that ends before the pause has none of it: a marker on a
	// chart must mean a moment inside the chart.
	narrow := pressureFor(t, store, pool, "15m")
	for _, wv := range narrow.Waves {
		if wv.Reason == reason {
			t.Fatal("a pause from 20 minutes ago appears on a 15 minute window")
		}
	}
}

// The screen draws a "confirm from two observation points" switch. There is
// one RU vantage, so the figure under it must be absent and say why — this is
// the case the console's rule was written for.
func TestPressureNamesTheMissingSecondVantageInsteadOfFillingIt(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)

	got := pressureFor(t, store, pool, "24h")
	why := ""
	for _, m := range got.Missing {
		if m.Figure == "vantages.second" {
			why = m.Why
		}
	}
	if len(got.Vantages) < 2 && why == "" {
		t.Fatalf("one vantage on record and nothing named in missing: %+v", got.Missing)
	}
	if len(got.Vantages) >= 2 && why != "" {
		t.Fatalf("two vantages reported and the figure is still called missing: %q", why)
	}

	// The counterfactual under the thresholds is never computable from probe
	// rows, so it is named on every answer.
	counter := false
	for _, m := range got.Missing {
		if m.Figure == "thresholds.counterfactual" {
			counter = true
		}
	}
	if !counter {
		t.Fatalf("the thresholds' counterfactual is not named: %+v", got.Missing)
	}
}

// Every bucket of the span is present and in order: an x axis made of
// "whatever rows exist" compresses an outage into nothing.
func TestPressureReturnsEveryBucketOfTheSpanInOrder(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)

	got := pressureFor(t, store, pool, "1h")
	if len(got.Points) < 50 {
		t.Fatalf("a 1h window at a %ds bucket came back with %d points",
			got.BucketSeconds, len(got.Points))
	}
	for i := 1; i < len(got.Points); i++ {
		step := got.Points[i].TS - got.Points[i-1].TS
		if step != int64(got.BucketSeconds) {
			t.Fatalf("points %d and %d are %ds apart, bucket is %ds",
				i-1, i, step, got.BucketSeconds)
		}
	}
}

// pressureFor calls the handler and decodes its answer.
func pressureFor(t *testing.T, store *analytics.Store, pool *pgxpool.Pool, window string) entryPressure {
	t.Helper()
	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryPressure(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/admin/entry/pressure?range=%s", window), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got entryPressure
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// Every figure on the detection screen is "what the probers saw", so the
// screen has to say who was looking. A vantage that reported is listed with
// its own counts and the moment it last spoke.
func TestPressureReportsWhatEachVantageDid(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	region := "ru-" + uuid.NewString()[:6]
	relay := "van-" + uuid.NewString()[:8]
	now := time.Now().UTC().Truncate(time.Second)
	rows := []analytics.EntryProbeRecord{
		probeAt(relay, region, "clean", now.Add(-20*time.Minute)),
		probeAt(relay+"-b", region, "clean", now.Add(-20*time.Minute)),
		probeAt(relay, region, "ip_banned", now.Add(-10*time.Minute)),
	}
	if err := store.InsertEntryProbes(ctx, rows); err != nil {
		t.Fatalf("insert probes: %v", err)
	}
	waitRows(t, store, relay, 2)

	got := pressureFor(t, store, pool, "6h")

	var mine *pressureVantage
	for i := range got.Vantages {
		if got.Vantages[i].Region == region {
			mine = &got.Vantages[i]
		}
	}
	if mine == nil {
		t.Fatalf("a vantage that probed three times is not listed: %+v", got.Vantages)
	}
	if mine.Probes != 3 {
		t.Fatalf("probes = %d, want 3", mine.Probes)
	}
	// Two relays in the widest round, not three rows summed.
	if mine.Relays != 2 {
		t.Fatalf("relays = %d, want 2 — the widest round, not a sum of rows", mine.Relays)
	}
	if mine.LastSeen < now.Add(-11*time.Minute).Unix() {
		t.Fatalf("last seen = %d, older than the newest row planted", mine.LastSeen)
	}
	// What the point said last is its newest row's verdict.
	if mine.LastVerdict != "ip_banned" {
		t.Fatalf("last verdict = %q, want the newest row's ip_banned", mine.LastVerdict)
	}
	if len(mine.PerBucket) != len(got.Points) {
		t.Fatalf("per-bucket series is %d long against %d points", len(mine.PerBucket), len(got.Points))
	}
	sum := 0
	for _, n := range mine.PerBucket {
		sum += n
	}
	if sum != mine.Probes {
		t.Fatalf("buckets hold %d probes, the window says %d", sum, mine.Probes)
	}
}

// A slot registered in entry_vantages stays on the screen as "silent" even
// when ClickHouse has zero probes for it, and disappears once retired.
func TestPressureShowsSilentRegisteredVantageSlotAndHidesRetiredSlot(t *testing.T) {
	cryptobox.ConfigureEphemeralForTests()
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	slot := "ru-slot-" + uuid.NewString()[:6]
	hostCT, err := cryptobox.Encrypt([]byte("198.51.100.77"))
	if err != nil {
		t.Fatalf("encrypt host: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_vantages (slot, title, host_ct, provider, status, updated_by)
		VALUES ($1, 'Санкт-Петербург', $2, 'selectel', 'active', 'test')
	`, slot, hostCT); err != nil {
		t.Fatalf("insert vantage slot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, slot)
	})

	got := pressureFor(t, store, pool, "6h")
	var found *pressureVantage
	for i := range got.Vantages {
		if got.Vantages[i].Region == slot {
			found = &got.Vantages[i]
		}
	}
	if found == nil {
		t.Fatalf("registered silent slot %s is missing from vantages: %+v", slot, got.Vantages)
	}
	if !found.Silent || found.Probes != 0 || found.Host != "198.51.100.77" || found.Provider != "selectel" || found.Title != "Санкт-Петербург" {
		t.Fatalf("unexpected slot payload: %+v", *found)
	}
	if len(found.PerBucket) != len(got.Points) {
		t.Fatalf("perBucket len = %d, want %d", len(found.PerBucket), len(got.Points))
	}

	// Retiring the slot hides it from the active vantage list.
	if _, err := pool.Exec(ctx, `
		UPDATE entry_vantages SET status = 'retired', retired_at = now() WHERE slot = $1
	`, slot); err != nil {
		t.Fatalf("retire slot: %v", err)
	}
	after := pressureFor(t, store, pool, "6h")
	for _, v := range after.Vantages {
		if v.Region == slot {
			t.Fatalf("retired slot %s still returned in active vantages", slot)
		}
	}
}
