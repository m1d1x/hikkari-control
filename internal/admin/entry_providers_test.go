package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedCompareBox writes one machine with an age, a price and, when it was
// handed back, a reason — which is the column 064 added and the whole point of
// the comparison.
func seedCompareBox(t *testing.T, pool *pgxpool.Pool, name, provider string, ageDays int, monthlyCents *int, retiredAfterDays int, reason string) {
	t.Helper()
	created := time.Now().UTC().Add(-time.Duration(ageDays) * 24 * time.Hour)
	var retiredAt *time.Time
	var retiredReason *string
	if reason != "" {
		at := created.Add(time.Duration(retiredAfterDays) * 24 * time.Hour)
		retiredAt = &at
		retiredReason = &reason
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_boxes
			(name, address_ct, provider, location, proto, created_at, monthly_cents, retired_at, retired_reason)
		VALUES ($1, '\x00', $2, 'amsterdam', 'vless-reality-vision', $3, $4, $5, $6)
		ON CONFLICT (name) DO NOTHING
	`, name, provider, created, monthlyCents, retiredAt, retiredReason); err != nil {
		t.Fatalf("seed compare box: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_relays WHERE box = $1`, name)
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_boxes WHERE name = $1`, name)
	})
}

// seedBannedRelay puts an ip ban on a machine's relay at a given age, which is
// the other place a ban is recorded besides the box's own reason.
func seedBannedRelay(t *testing.T, pool *pgxpool.Pool, name, box string, diedDaysAgo int) {
	t.Helper()
	died := time.Now().UTC().Add(-time.Duration(diedDaysAgo) * 24 * time.Hour)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_relays (name, kind, status, box, identity_ct, created_at, died_at, died_reason)
		VALUES ($1, 'personal', 'dead', $2, '\x00', $3, $3, 'ip_banned')
		ON CONFLICT (name) DO NOTHING
	`, name, box, died); err != nil {
		t.Fatalf("seed banned relay: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_relays WHERE name = $1`, name)
	})
}

func fetchCompare(t *testing.T, h *Handlers) entryProviderCompare {
	t.Helper()
	rec := httptest.NewRecorder()
	h.EntryProviderCompare(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/providers/compare", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res entryProviderCompare
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return res
}

func statsOf(t *testing.T, res entryProviderCompare, provider string) providerStats {
	t.Helper()
	for _, p := range res.Hosting {
		if p.Provider == provider {
			return p
		}
	}
	t.Fatalf("no provider %q in %+v", provider, res.Hosting)
	return providerStats{}
}

// boxTag is a letters-only prefix, because a box name is letters then digits
// (039: `^[a-z]+[0-9]+$`) and a unique run tag has to fit inside the letters.
func boxTag(prefix string) string {
	n := time.Now().UnixNano() % 100_000
	out := []byte(prefix)
	for n > 0 {
		out = append(out, byte('a'+n%10))
		n /= 10
	}
	return string(out)
}

func TestEntryProviderCompare(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	tag := boxTag("cmp")
	good := tag + "good"
	bad := tag + "bad"

	price := 600 // $6/mo

	// The good provider: three machines older than the window, none banned
	// inside it, one handed back healthy after 40 days.
	seedCompareBox(t, pool, tag+"g1", good, 50, &price, 0, "")
	seedCompareBox(t, pool, tag+"g2", good, 45, &price, 0, "")
	seedCompareBox(t, pool, tag+"g3", good, 60, &price, 40, "rotated")

	// The bad provider: two machines older than the window, both banned
	// inside it — one recorded on the box, one only on its relay, which the
	// winrate has to catch either way.
	seedCompareBox(t, pool, tag+"b1", bad, 50, &price, 10, "ip_banned")
	seedCompareBox(t, pool, tag+"b2", bad, 40, &price, 0, "")
	seedBannedRelay(t, pool, tag+"b2-r1", tag+"b2", 25)

	// A machine too young to be asked the window's question at all.
	seedCompareBox(t, pool, tag+"g4", good, 3, &price, 0, "")

	res := fetchCompare(t, h)

	if res.WindowDays != compareWindow {
		t.Errorf("windowDays = %d, want %d", res.WindowDays, compareWindow)
	}

	g := statsOf(t, res, good)
	if g.BoughtCount != 4 {
		t.Errorf("good boughtCount = %d, want 4", g.BoughtCount)
	}
	if g.AliveCount != 3 {
		t.Errorf("good aliveCount = %d, want 3", g.AliveCount)
	}
	if g.CohortCount != 3 {
		t.Errorf("good cohortCount = %d, want 3 (the young machine is not asked)", g.CohortCount)
	}
	if g.WinratePct == nil || *g.WinratePct != 100 {
		t.Errorf("good winratePct = %v, want 100", g.WinratePct)
	}
	if g.RotatedCount != 1 {
		t.Errorf("good rotatedCount = %d, want 1", g.RotatedCount)
	}
	if g.BannedCount != 0 {
		t.Errorf("good bannedCount = %d, want 0", g.BannedCount)
	}
	if g.PerBoxMonthlyCents == nil || *g.PerBoxMonthlyCents != price {
		t.Errorf("good perBoxMonthlyCents = %v, want %d", g.PerBoxMonthlyCents, price)
	}
	// Its one retired machine was handed back healthy: let go, not burned.
	// A life cut short by the owner's own rotation is no life the censor
	// ended, and it must not put a short "IP life" on a hoster that never had
	// a ban.
	if g.MedianLifeDays != nil || g.LifeSampleCount != 0 || g.CostPerLivingDayCents != nil {
		t.Errorf("good provider shows a life (%v days over %d, %v cents/day) though nothing was burned",
			ptrVal(g.MedianLifeDays), g.LifeSampleCount, ptrVal(g.CostPerLivingDayCents))
	}
	if len(g.BansDaily) != compareWindow {
		t.Errorf("good bansDaily = %d days, want %d", len(g.BansDaily), compareWindow)
	}
	if g.BansWindow != 0 {
		t.Errorf("good bansWindow = %d, want 0", g.BansWindow)
	}

	b := statsOf(t, res, bad)
	if b.CohortCount != 2 {
		t.Errorf("bad cohortCount = %d, want 2", b.CohortCount)
	}
	if b.SurvivedCount != 0 {
		t.Errorf("bad survivedCount = %d, want 0: one ban is on the box, one on its relay", b.SurvivedCount)
	}
	if b.WinratePct == nil || *b.WinratePct != 0 {
		t.Errorf("bad winratePct = %v, want 0", b.WinratePct)
	}
	if b.BannedCount != 1 {
		t.Errorf("bad bannedCount = %d, want 1 (the one handed back over the ban)", b.BannedCount)
	}
	if b.BansWindow != 1 {
		t.Errorf("bad bansWindow = %d, want 1 (the relay ip-banned 25 days ago)", b.BansWindow)
	}
	// The banned machine lived 10 days before the censor took its address.
	if b.MedianLifeDays == nil || *b.MedianLifeDays < 9 || *b.MedianLifeDays > 11 {
		t.Errorf("bad medianLifeDays = %v, want ~10", ptrVal(b.MedianLifeDays))
	}
	if b.LifeSampleCount != 1 {
		t.Errorf("bad lifeSampleCount = %d, want 1 (living machines are not finished lives)", b.LifeSampleCount)
	}
	// $6 a month over a 10-day life is 60 cents of machine per living day.
	if b.CostPerLivingDayCents == nil || *b.CostPerLivingDayCents < 58 || *b.CostPerLivingDayCents > 62 {
		t.Errorf("bad costPerLivingDayCents = %v, want ~60", ptrVal(b.CostPerLivingDayCents))
	}
	// Two machines rented for the whole 30-day window at $6, two more for the
	// part of it they lived: the accrued rent is real money, not zero.
	if b.SpentCents <= 0 {
		t.Errorf("bad spentCents = %d, want the rent accrued over the window", b.SpentCents)
	}
}

// TestEntryProviderCompareNoPurchases is the section's rule: a provider the
// hub never bought from has no winrate, and nothing is invented for it.
func TestEntryProviderCompareNoPurchases(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	tag := boxTag("nop")
	// One machine, younger than the window: it cannot answer the window's
	// question, so there is no winrate — but it is a bought machine, so the
	// count of those is not nil.
	seedCompareBox(t, pool, tag+"1", tag+"prov", 2, nil, 0, "")

	res := fetchCompare(t, h)
	p := statsOf(t, res, tag+"prov")

	if p.BoughtCount != 1 {
		t.Errorf("boughtCount = %d, want 1", p.BoughtCount)
	}
	if p.CohortCount != 0 {
		t.Errorf("cohortCount = %d, want 0", p.CohortCount)
	}
	if p.WinratePct != nil {
		t.Errorf("winratePct = %v, want nil with nobody old enough to ask", *p.WinratePct)
	}
	if p.MedianLifeDays != nil {
		t.Errorf("medianLifeDays = %v, want nil with nothing handed back", *p.MedianLifeDays)
	}
	if p.PerBoxMonthlyCents != nil {
		t.Errorf("perBoxMonthlyCents = %v, want nil with no price on record", *p.PerBoxMonthlyCents)
	}
	if p.CostPerLivingDayCents != nil {
		t.Errorf("costPerLivingDayCents = %v, want nil without a price and a life", *p.CostPerLivingDayCents)
	}
	if p.PricelessBoxes != 1 {
		t.Errorf("pricelessBoxes = %d, want 1", p.PricelessBoxes)
	}
	if len(res.Missing) == 0 {
		t.Error("missing is empty although a live machine carries no price")
	}
}

// TestRetireWritesReason is the migration's other half: the retire path must
// write the reason, because the comparison reads it.
func TestRetireWritesReason(t *testing.T) {
	pool := livePostgres(t)

	tag := boxTag("ret")
	seedCompareBox(t, pool, tag+"1", tag+"prov", 10, nil, 0, "")

	// The constraint 064 adds: a retired box without a reason is refused by
	// the database, so no path can quietly leave it out.
	_, err := pool.Exec(context.Background(),
		`UPDATE entry_boxes SET retired_at = now() WHERE name = $1`, tag+"1")
	if err == nil {
		t.Error("retiring a box without a reason was accepted; the 064 constraint is missing")
	}

	if _, err := pool.Exec(context.Background(),
		`UPDATE entry_boxes SET retired_at = now(), retired_reason = 'rotated' WHERE name = $1`, tag+"1"); err != nil {
		t.Fatalf("retire with a reason: %v", err)
	}
}

// ptrVal prints a nullable figure as its value or "nil".
func ptrVal(v *float64) any {
	if v == nil {
		return "nil"
	}
	return *v
}
