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

func seedFleetBox(t *testing.T, pool *pgxpool.Pool, name, proto string) string {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
		VALUES ($1, '\x00', 'vultr', 'amsterdam', $2)
		ON CONFLICT (name) DO NOTHING
	`, name, proto); err != nil {
		t.Fatalf("seed fleet box: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_boxes WHERE name = $1`, name)
	})
	return name
}

func seedFleetRelay(t *testing.T, pool *pgxpool.Pool, name, box, status string) {
	t.Helper()
	var (
		diedAt     *time.Time
		diedReason *string
		keyID      *string
		envelopeCt []byte
		issuedAt   *time.Time
		expiresAt  *time.Time
		signedAt   *time.Time
	)
	if status == "dead" {
		now := time.Now().UTC()
		diedAt = &now
		r := "ip_banned"
		diedReason = &r
	}
	if status != "pending" {
		k := "k1"
		keyID = &k
		envelopeCt = []byte{0}
		now := time.Now().UTC()
		exp := now.Add(30 * 24 * time.Hour)
		issuedAt = &now
		expiresAt = &exp
		signedAt = &now
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_relays (name, kind, status, key_id, envelope_ct, issued_at, expires_at, signed_at, box, identity_ct, died_at, died_reason)
		VALUES ($1, 'personal', $2, $3, $4, $5, $6, $7, $8, '\x00', $9, $10)
		ON CONFLICT (name) DO NOTHING
	`, name, status, keyID, envelopeCt, issuedAt, expiresAt, signedAt, box, diedAt, diedReason); err != nil {
		t.Fatalf("seed fleet relay: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_relays WHERE name = $1`, name)
	})
}

func TestEntryFleet(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	tag := fmt.Sprintf("fb%d", time.Now().UnixNano()%100_000)
	box1 := seedFleetBox(t, pool, tag+"1", "vless-reality-vision")
	box2 := seedFleetBox(t, pool, tag+"2", "vless-xhttp")

	seedFleetRelay(t, pool, tag+"r1", box1, "alive")
	seedFleetRelay(t, pool, tag+"r2", box1, "pending")
	seedFleetRelay(t, pool, tag+"r3", box2, "dead")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/machines?window=7d", nil)
	h.EntryFleet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var res entryFleet
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if res.Window != "7d" {
		t.Errorf("window = %q, want 7d", res.Window)
	}
	if res.Pool.InDistribution < 1 {
		t.Errorf("inDistribution = %d, want >= 1", res.Pool.InDistribution)
	}
	if res.Pool.PendingCount < 1 {
		t.Errorf("pendingCount = %d, want >= 1", res.Pool.PendingCount)
	}
	if res.Strip.DeathsVsReplacements.DeathsDay < 1 {
		t.Errorf("deathsDay = %d, want the ip ban counted", res.Strip.DeathsVsReplacements.DeathsDay)
	}
	if n := len(res.Pool.Series); n < 28 || n > 29 {
		t.Errorf("pool series has %d points, want one per 6 hours of the week", n)
	}
	// The seed's identity is no seal: the relay says it cannot be read, and
	// shows no port or cover rather than a made-up one.
	for _, b := range res.Boxes {
		for _, r := range b.Relays {
			if r.Name == tag+"r1" && (!r.Unreadable || r.Port != 0 || r.Cover != "") {
				t.Errorf("unreadable relay shown as %+v", r)
			}
		}
	}
	if len(res.Boxes) < 2 {
		t.Errorf("boxes count = %d, want >= 2", len(res.Boxes))
	}
	// The provider comparison rides in the same answer: one request a screen.
	found := false
	for _, p := range res.Providers.Hosting {
		if p.Provider == "vultr" && p.AliveCount >= 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("providers = %+v, want vultr with the seeded machines", res.Providers.Hosting)
	}
}

// A machine handed back on purpose kills its relays with "rotated". The
// screen's «блокировки в сутки» counts what the censor did, and a rotation is
// not that — it used to be, and every manual hand-back read as a block.
func TestEntryFleetCountsOnlyCensorDeathsAsBlocks(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}
	fleet := func() entryFleet {
		t.Helper()
		rec := httptest.NewRecorder()
		h.EntryFleet(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/machines?window=24h", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var res entryFleet
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return res
	}

	tag := fmt.Sprintf("fc%d", time.Now().UnixNano()%100_000)
	box := seedFleetBox(t, pool, tag, "vless-reality-vision")
	before := fleet().Strip.DeathsVsReplacements.DeathsDay

	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_relays (name, kind, status, box, identity_ct, died_at, died_reason)
		VALUES ($1, 'personal', 'dead', $2, '\x00', now(), 'rotated')
	`, tag+"-1", box); err != nil {
		t.Fatalf("seed rotated relay: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_relays WHERE name = $1`, tag+"-1")
	})

	res := fleet()
	if res.Strip.DeathsVsReplacements.DeathsDay != before {
		t.Fatalf("a rotation moved blocks per day %d → %d", before, res.Strip.DeathsVsReplacements.DeathsDay)
	}
	var inSeries int
	for _, b := range res.Strip.DeathsVsReplacements.Series {
		inSeries += b.Deaths
	}
	if inSeries != res.Strip.DeathsVsReplacements.DeathsDay {
		t.Fatalf("the 24h series sums to %d blocks, the headline says %d", inSeries, res.Strip.DeathsVsReplacements.DeathsDay)
	}
}
