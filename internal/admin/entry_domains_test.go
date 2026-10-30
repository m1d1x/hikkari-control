package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedDomain writes one ledger row. A burned name needs both timestamps: the
// screen's whole subject is the span between them.
func seedDomain(t *testing.T, pool *pgxpool.Pool, domain, tld, status string, assigned, burned *time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_domains (domain, tld, status, source, cost_cents, assigned_at, burned_at)
		VALUES ($1, $2, $3, 'purchased', 300, $4, $5)
		ON CONFLICT (domain) DO NOTHING
	`, domain, tld, status, assigned, burned); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_domains WHERE domain = $1`, domain)
	})
}

func fetchDomainMetrics(t *testing.T, h *Handlers, window string) entryDomainMetrics {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/domains/metrics?window="+window, nil)
	h.EntryDomainMetrics(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res entryDomainMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return res
}

func TestEntryDomainMetrics(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	tag := fmt.Sprintf("d%d", time.Now().UnixNano()%100_000)
	now := time.Now().UTC()

	// Three burned .mom names, 24h / 48h / 36h of life, all inside 7 days.
	for i, hours := range []int{24, 48, 36} {
		burned := now.Add(-time.Duration(i+1) * 24 * time.Hour)
		assigned := burned.Add(-time.Duration(hours) * time.Hour)
		seedDomain(t, pool, fmt.Sprintf("%s-mom-%d.mom", tag, i), ".mom", "burned", &assigned, &burned)
	}
	// One burned .top name that lived 12h.
	topBurned := now.Add(-36 * time.Hour)
	topAssigned := topBurned.Add(-12 * time.Hour)
	seedDomain(t, pool, tag+"-top.top", ".top", "burned", &topAssigned, &topBurned)
	// Two names in the pool, one on a box.
	seedDomain(t, pool, tag+"-free1.mom", ".mom", "owned", nil, nil)
	seedDomain(t, pool, tag+"-free2.mom", ".mom", "owned", nil, nil)
	assignedOnly := now.Add(-5 * time.Hour)
	seedDomain(t, pool, tag+"-live.mom", ".mom", "assigned", &assignedOnly, nil)

	res := fetchDomainMetrics(t, h, "7d")

	if res.Window != "7d" {
		t.Errorf("window = %q, want 7d", res.Window)
	}
	if res.Life.SampleCount < 4 {
		t.Errorf("sampleCount = %d, want >= 4 (the four burned names)", res.Life.SampleCount)
	}
	if res.Life.MedianHours == nil {
		t.Fatal("medianHours is nil although four names burned in the window")
	}
	if *res.Life.MedianHours <= 0 {
		t.Errorf("medianHours = %v, want > 0", *res.Life.MedianHours)
	}
	if len(res.Life.Days) != 7 {
		t.Errorf("days = %d, want 7 buckets for a 7d window", len(res.Life.Days))
	}

	// The zones must carry the per-day line, one slot per day bucket, and a
	// day with no burned name of that zone must stay nil rather than zero.
	var mom *domainZone
	for i := range res.Life.Zones {
		if len(res.Life.Zones[i].Daily) != len(res.Life.Days) {
			t.Errorf("zone %s: daily len = %d, want %d", res.Life.Zones[i].TLD,
				len(res.Life.Zones[i].Daily), len(res.Life.Days))
		}
		if res.Life.Zones[i].TLD == ".mom" {
			mom = &res.Life.Zones[i]
		}
	}
	if mom == nil {
		t.Fatalf("no .mom zone in %+v", res.Life.Zones)
	}
	if mom.SampleCount < 3 {
		t.Errorf(".mom sampleCount = %d, want >= 3", mom.SampleCount)
	}
	if mom.MedianHours == nil || *mom.MedianHours < 24 || *mom.MedianHours > 48 {
		t.Errorf(".mom medianHours = %v, want between 24 and 48", mom.MedianHours)
	}
	filled := 0
	for _, v := range mom.Daily {
		if v != nil {
			filled++
		}
	}
	if filled == 0 {
		t.Error(".mom daily is empty although three names burned on three different days")
	}
	if filled == len(mom.Daily) {
		t.Error(".mom daily has no gaps although only three of seven days hold a burn")
	}

	// The pool half.
	if res.Pool.Owned < 2 {
		t.Errorf("owned = %d, want >= 2", res.Pool.Owned)
	}
	if res.Pool.Assigned < 1 {
		t.Errorf("assigned = %d, want >= 1", res.Pool.Assigned)
	}
	if res.Pool.BurnedWindow < 4 {
		t.Errorf("burnedWindow = %d, want >= 4", res.Pool.BurnedWindow)
	}
	if res.Pool.BurnedPerDay == nil {
		t.Fatal("burnedPerDay is nil although four names burned")
	}
	if res.Pool.HoursLeft == nil {
		t.Error("hoursLeft is nil although the pool holds names and the rate is above zero")
	}

	// The gap the ledger cannot close must be named, not guessed.
	if len(res.Missing) == 0 {
		t.Error("missing is empty: a domain has no cause of death and the payload must say so")
	}
}

// TestEntryDomainMetricsEmpty is the rule the screen exists under: a figure the
// hub cannot compute is absent, never a plausible default.
func TestEntryDomainMetricsEmpty(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	// Nothing seeded for this window: a 90d window over a database whose
	// burned rows are older, or empty, must come back nil rather than 38.
	var burnedInWindow int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM entry_domains
		WHERE status = 'burned' AND assigned_at IS NOT NULL AND burned_at IS NOT NULL
		  AND burned_at >= now() - interval '90 days'
	`).Scan(&burnedInWindow); err != nil {
		t.Fatalf("count burned: %v", err)
	}
	if burnedInWindow > 0 {
		t.Skip("the database holds burned names in the window — nothing to assert about emptiness")
	}

	res := fetchDomainMetrics(t, h, "90d")
	if res.Life.MedianHours != nil {
		t.Errorf("medianHours = %v, want nil with nothing burned", *res.Life.MedianHours)
	}
	if res.Life.TrendHours != nil {
		t.Errorf("trendHours = %v, want nil with nothing burned", *res.Life.TrendHours)
	}
	if res.Pool.BurnedPerDay != nil {
		t.Errorf("burnedPerDay = %v, want nil with nothing burned", *res.Pool.BurnedPerDay)
	}
	if res.Pool.HoursLeft != nil {
		t.Errorf("hoursLeft = %v, want nil with nothing burned", *res.Pool.HoursLeft)
	}
	if len(res.Life.Days) != 90 {
		t.Errorf("days = %d, want 90 buckets even with no samples", len(res.Life.Days))
	}
}

func TestDomainWindow(t *testing.T) {
	for _, tc := range []struct {
		in    string
		days  int
		label string
		ok    bool
	}{
		{"", 30, "30d", true},
		{"7d", 7, "7d", true},
		{"30d", 30, "30d", true},
		{"90d", 90, "90d", true},
		{"24h", 0, "", false},
		{"1y", 0, "", false},
	} {
		days, label, ok := domainWindow(tc.in)
		if days != tc.days || label != tc.label || ok != tc.ok {
			t.Errorf("domainWindow(%q) = (%d, %q, %v), want (%d, %q, %v)",
				tc.in, days, label, ok, tc.days, tc.label, tc.ok)
		}
	}
}

// The shape of a name is a product rule, so it is pinned by example rather
// than described: these are the ways the generator and the registrar's
// suggestions actually build names.
func TestClassifyDomainShape(t *testing.T) {
	cases := map[string]string{
		"cedar4821.lol":        "word_digits",
		"mapleproxy.top":       "long_word",
		"img-assets-cache.net": "hyphenated",
		"49281772.xyz":         "digits",
		"rivr.mom":             "short_word",
		"CEDAR4821.LOL":        "word_digits",
	}
	for domain, want := range cases {
		if got := classifyDomainShape(domain); got != want {
			t.Errorf("classifyDomainShape(%q) = %q, want %q", domain, got, want)
		}
	}
}

// A family with a handful of names has no median worth ranking by: three
// domains that happened to die early would read as "never buy this kind
// again". The count is reported, the median is withheld until the sample is
// big enough to mean something.
func TestDomainShapesWithholdTheMedianUntilTheSampleIsBigEnough(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}
	now := time.Now().UTC()

	// Six of one family, two of another, all burned inside the window.
	for i := 0; i < 6; i++ {
		assigned := now.Add(-time.Duration(60+i) * time.Hour)
		burned := assigned.Add(40 * time.Hour)
		seedDomain(t, pool, fmt.Sprintf("shapetest%d.lol", i), ".lol", "burned", &assigned, &burned)
	}
	for i := 0; i < 2; i++ {
		assigned := now.Add(-time.Duration(50+i) * time.Hour)
		burned := assigned.Add(10 * time.Hour)
		seedDomain(t, pool, fmt.Sprintf("shape-test-rare-%d.lol", i), ".lol", "burned", &assigned, &burned)
	}

	got := fetchDomainMetrics(t, h, "30d")

	byShape := map[string]domainShape{}
	for _, s := range got.Shapes {
		byShape[s.Shape] = s
	}

	big, ok := byShape["word_digits"]
	if !ok {
		t.Fatalf("the family of six is missing: %+v", got.Shapes)
	}
	if big.SampleCount < 6 {
		t.Fatalf("word_digits sample = %d, want at least the six planted", big.SampleCount)
	}
	if big.MedianHours == nil {
		t.Fatal("a family of six has no median")
	}
	if big.Example == "" {
		t.Fatal("a family with a median shows no example of what it means")
	}

	small, ok := byShape["hyphenated"]
	if !ok {
		t.Fatalf("the family of two is missing: %+v", got.Shapes)
	}
	if small.SampleCount >= shapeMinSamples {
		t.Skipf("other rows pushed hyphenated to %d samples; nothing to assert", small.SampleCount)
	}
	if small.MedianHours != nil {
		t.Fatalf("a family of %d reports a median of %v", small.SampleCount, *small.MedianHours)
	}

	// The ranking is the recommendation: a family with no median must not sit
	// above a measured one.
	seenUnranked := false
	for _, s := range got.Shapes {
		if s.MedianHours == nil {
			seenUnranked = true
			continue
		}
		if seenUnranked {
			t.Fatalf("a measured family (%s) sits below an unranked one: %+v", s.Shape, got.Shapes)
		}
	}
}
