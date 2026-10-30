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

	"github.com/m1d1x/hikkari-control/internal/analytics"
)

func overview(t *testing.T, h *Handlers, window string) entryOverview {
	t.Helper()
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window="+window, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// A stand deployed ten minutes ago has ten minutes of data, and the 24h
// window draws it: hourly buckets, the current one measured. The screen used
// to bucket by calendar day and generate a curve until it had six of them.
func TestEntryOverviewSeriesDrawsTheFirstHour(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	account := uuid.NewString()[:32]
	relay := "ovws" + uuid.NewString()[:6] + "-1"
	at := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Minute)
	if err := store.InsertEntryReach(ctx, []analytics.EntryReachRecord{
		reachRow(account, "relay", relay, 1, 40, 800, at),
	}); err != nil {
		t.Fatalf("insert reach: %v", err)
	}
	const gb = uint64(1024 * 1024 * 1024)
	if err := store.InsertEntryRelayStats(ctx, []analytics.EntryRelayStatRecord{
		relayStat(relay, 1*gb, 1*gb, 60, at.Add(-time.Minute)),
		relayStat(relay, 2*gb, 3*gb, 120, at),
	}); err != nil {
		t.Fatalf("insert pulses: %v", err)
	}
	waitReachRows(t, store, account, 1)
	waitStatRows(t, store, relay, 2)

	got := overview(t, &Handlers{analytics: store, pool: pool}, "24h")

	if got.BucketSeconds != 3600 {
		t.Fatalf("bucket = %ds, want an hour for the 24h window", got.BucketSeconds)
	}
	if n := len(got.Reach.Series); n < 24 || n > 25 {
		t.Fatalf("reach series has %d points, want one per hour of the window", n)
	}
	if len(got.Money.Series) != len(got.Reach.Series) || len(got.Survival.Series) != len(got.Reach.Series) {
		t.Fatalf("series do not share one axis: reach %d, money %d, survival %d",
			len(got.Reach.Series), len(got.Money.Series), len(got.Survival.Series))
	}
	current := time.Now().UTC().Truncate(time.Hour).Format(time.RFC3339)
	last := got.Reach.Series[len(got.Reach.Series)-1]
	if last.Bucket != current || last.AnyPct == nil || last.Attempts < 1 {
		t.Fatalf("the current hour is not drawn: %+v (want bucket %s)", last, current)
	}
	if got.Traffic == nil || len(got.Traffic.Series) != len(got.Reach.Series) {
		t.Fatalf("traffic series missing or off the shared axis: %+v", got.Traffic)
	}
	tlast := got.Traffic.Series[len(got.Traffic.Series)-1]
	if tlast.GBUp == nil || *tlast.GBUp < 1 || tlast.GBDown == nil || *tlast.GBDown < 2 {
		t.Fatalf("the current hour carried 1 GB up and 2 GB down, the series says %+v", tlast)
	}
	if got.Traffic.GBUp < 1 || got.Traffic.GBDown < 2 {
		t.Fatalf("headline split up %.2f / down %.2f, want at least 1 / 2", got.Traffic.GBUp, got.Traffic.GBDown)
	}
}

// A machine handed back on purpose is no ban. The survival block counts what
// the censor killed; a rotation that it counted would make every manual
// hand-back read as the censor's work.
func TestEntryOverviewSurvivalCountsOnlyTheCensor(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()
	h := &Handlers{analytics: store, pool: pool}

	tag := fmt.Sprintf("ovc%d", time.Now().UnixNano()%1_000_000)
	box := seedFleetBox(t, pool, tag, "vless-reality-vision")
	before := overview(t, h, "24h").Survival.CensorDeaths

	kill := func(name, reason string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO entry_relays (name, kind, status, box, identity_ct, created_at, died_at, died_reason)
			VALUES ($1, 'personal', 'dead', $2, '\x00', now() - interval '2 days', now(), $3)
		`, name, box, reason); err != nil {
			t.Fatalf("seed relay: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM entry_relays WHERE name = $1`, name)
		})
	}

	kill(tag+"-1", "rotated")
	kill(tag+"-2", "provider_down")
	if got := overview(t, h, "24h").Survival.CensorDeaths; got != before {
		t.Fatalf("a rotation and a hoster outage moved censor deaths %d → %d", before, got)
	}

	kill(tag+"-3", "ip_banned")
	got := overview(t, h, "24h")
	if got.Survival.CensorDeaths != before+1 {
		t.Fatalf("an ip ban moved censor deaths %d → %d, want +1", before, got.Survival.CensorDeaths)
	}
	last := got.Survival.Series[len(got.Survival.Series)-1]
	if last.Deaths < 1 || last.RelayMedianDays == nil {
		t.Fatalf("the ban is not in the current bucket: %+v", last)
	}
}
