package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// askBoxCard asks the card of one machine.
func askBoxCard(t *testing.T, h *Handlers, box, window string) (int, entryBoxCard) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/machines/"+box+"?window="+window, nil)
	req.SetPathValue("name", box)
	rec := httptest.NewRecorder()
	h.EntryBoxCard(rec, req)
	var card entryBoxCard
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, card
}

func cardNames(card entryBoxCard, figure string) bool {
	for _, m := range card.Missing {
		if m.Figure == figure {
			return true
		}
	}
	return false
}

// waitPulseRows blocks until a relay's pulses are readable: the insert is
// async, so wait for the rows rather than guess a delay.
func waitPulseRows(t *testing.T, store *analytics.Store, relay string, n uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var got uint64
		if err := store.QueryRow(context.Background(), &got,
			"SELECT count() FROM entry_relay_stats WHERE relay_name = ?", relay); err != nil {
			t.Fatalf("count: %v", err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pulses of %s never became readable (have %d)", n, relay, got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The card is a machine's whole life: born, admitted, its incidents, its
// death and why, how long it lived and what it cost over that life — every
// one of them computed from the rows, nothing kept as a counter. The address
// is opened here and nowhere in the list.
func TestEntryBoxCardTellsTheMachinesLifeAndCost(t *testing.T) {
	pool := livePostgres(t)
	cryptobox.ConfigureEphemeralForTests()
	ctx := context.Background()
	h := &Handlers{pool: pool}

	tag := fmt.Sprintf("bc%d", time.Now().UnixNano()%100_000)
	addrCT, err := cryptobox.Encrypt([]byte("203.0.113.41"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Lived ten days at $6 a month, then handed back after an ip ban.
	born := time.Now().UTC().Add(-10 * 24 * time.Hour)
	died := born.Add(10 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_boxes (name, address_ct, provider, location, proto, monthly_cents, created_at, retired_at, retired_reason)
		VALUES ($1, $2, 'vultr', 'amsterdam', 'vless-reality-vision', 600, $3, $4, 'ip_banned')
	`, tag, addrCT, born, died); err != nil {
		t.Fatalf("seed box: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM entry_boxes WHERE name = $1`, tag) })
	seedFleetRelay(t, pool, tag+"-1", tag, "dead")
	if _, err := pool.Exec(ctx, `UPDATE entry_relays SET signed_at = $2 WHERE name = $1`, tag+"-1", born.Add(time.Hour)); err != nil {
		t.Fatalf("sign relay: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_incidents (box, cause, status, playbook, cost_cents, opened_at, closed_at, outcome)
		VALUES ($1, 'ip_banned', 'resolved', 'evict_box', 250, $2, $3, 'replaced')
	`, tag, died.Add(-time.Hour), died); err != nil {
		t.Fatalf("seed incident: %v", err)
	}

	code, card := askBoxCard(t, h, tag, "7d")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if card.Box.Address != "203.0.113.41" || card.Box.AddressUnreadable {
		t.Fatalf("address = %q (unreadable %v): the card is where it is opened", card.Box.Address, card.Box.AddressUnreadable)
	}
	hist := card.History
	if hist.AdmittedAt == nil || !hist.AdmittedAt.Equal(born.Add(time.Hour).Truncate(time.Microsecond)) {
		t.Fatalf("admitted at %v, want the first relay's signing", hist.AdmittedAt)
	}
	if hist.RetiredAt == nil || hist.RetiredReason != "ip_banned" {
		t.Fatalf("death = %v %q", hist.RetiredAt, hist.RetiredReason)
	}
	if d := hist.LifetimeSec - 10*24*3600; d < -1 || d > 1 {
		t.Fatalf("lifetime = %ds, want ten days", hist.LifetimeSec)
	}
	// $6 / 30 days × 10 days = $2.
	if hist.RentCents == nil || *hist.RentCents != 200 {
		t.Fatalf("rent over the life = %v, want 200 cents", hist.RentCents)
	}
	if hist.RepairCents != 250 || len(hist.Incidents) != 1 || hist.Incidents[0].Playbook != "evict_box" {
		t.Fatalf("incidents = %+v, repairs %d", hist.Incidents, hist.RepairCents)
	}
	if len(card.Relays) != 1 || card.Relays[0].DiedReason != "ip_banned" || card.Relays[0].Verdict != "bad" {
		t.Fatalf("relays = %+v", card.Relays)
	}
	// Pulse fields the edge does not send are named, not zeroed.
	for _, f := range []string{"stats.activeSessions", "stats.handshakeFailures"} {
		if !cardNames(card, f) {
			t.Errorf("%s is not named in missing", f)
		}
	}
	// No analytics on this hub: the stats are absent and say why.
	if card.Stats != nil || !cardNames(card, "stats") {
		t.Fatalf("stats = %+v with no analytics", card.Stats)
	}

	// The list never carries an address.
	rec := httptest.NewRecorder()
	h.EntryFleet(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/machines?window=24h", nil))
	if strings.Contains(rec.Body.String(), "203.0.113.41") {
		t.Fatal("the fleet list carries a machine's address")
	}

	if code, _ := askBoxCard(t, h, tag+"x", "7d"); code != http.StatusNotFound {
		t.Fatalf("unknown box: status %d, want 404", code)
	}
}

// A machine rented by hand has no price: its rent over the life is absent and
// named, never zero.
func TestEntryBoxCardNamesTheRentOfAnUnpricedMachine(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}
	tag := fmt.Sprintf("bu%d", time.Now().UnixNano()%100_000)
	seedFleetBox(t, pool, tag, "vless-xhttp")

	_, card := askBoxCard(t, h, tag, "24h")
	if card.History.RentCents != nil || !cardNames(card, "history.rentCents") {
		t.Fatalf("rent = %v, named = %v", card.History.RentCents, cardNames(card, "history.rentCents"))
	}
	if card.History.AdmittedAt != nil || card.History.RetiredAt != nil {
		t.Fatalf("a fresh, never-admitted machine reads as %+v", card.History)
	}
}

// The relay stats are the pulse differenced pulse by pulse and summed over
// the machine's relays: bytes, new connections per second, CPU and memory,
// with the latest build each relay reported. A restart is a counter going
// down, never a negative delta. The people are the accounts whose socket came
// through the machine.
func TestEntryBoxCardDrawsThePulseOfItsRelays(t *testing.T) {
	pool := livePostgres(t)
	store := liveClickHouse(t)
	ctx := context.Background()
	h := &Handlers{pool: pool, analytics: store}

	tag := fmt.Sprintf("bp%d", time.Now().UnixNano()%100_000)
	seedFleetBox(t, pool, tag, "vless-reality-vision")
	relay := tag + "-1"
	seedFleetRelay(t, pool, relay, tag, "alive")

	const mb = 1024 * 1024
	// Three pulses inside one hour, the third after a restart.
	at := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour).Add(5 * time.Minute)
	pulses := []analytics.EntryRelayStatRecord{
		{Timestamp: at, RelayName: relay, Proto: "vless-reality-vision", UptimeS: 600,
			BytesUp: 1 * mb, BytesDown: 10 * mb, Conns: 100, CPUPct: 10, MemMB: 300, Version: "aaa111"},
		{Timestamp: at.Add(10 * time.Minute), RelayName: relay, Proto: "vless-reality-vision", UptimeS: 1200,
			BytesUp: 3 * mb, BytesDown: 30 * mb, Conns: 460, CPUPct: 30, MemMB: 340, Version: "aaa111"},
		{Timestamp: at.Add(20 * time.Minute), RelayName: relay, Proto: "vless-reality-vision", UptimeS: 60,
			BytesUp: 1 * mb, BytesDown: 4 * mb, Conns: 260, CPUPct: 20, MemMB: 320, Version: "bbb222"},
	}
	if err := store.InsertEntryRelayStats(ctx, pulses); err != nil {
		t.Fatalf("insert pulses: %v", err)
	}
	if err := store.InsertEntryUserTraffic(ctx, []analytics.EntryUserTrafficRecord{
		{Timestamp: at, Account: fmt.Sprintf("%032d", 7), Box: tag, BytesDown: 5},
		{Timestamp: at, Account: fmt.Sprintf("%032d", 8), Box: tag, BytesDown: 5},
		{Timestamp: at, Account: fmt.Sprintf("%032d", 8), Box: tag, BytesUp: 5},
	}); err != nil {
		t.Fatalf("insert user traffic: %v", err)
	}
	waitPulseRows(t, store, relay, 3)
	waitUserTrafficRows(t, store, 3)

	code, card := askBoxCard(t, h, tag, "24h")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if card.Stats == nil || cardNames(card, "stats") {
		t.Fatalf("stats absent with three pulses on record: %+v", card.Missing)
	}
	var pt *boxStatsPoint
	for i := range card.Stats.Series {
		if card.Stats.Series[i].Bucket == at.Truncate(time.Hour).Format(time.RFC3339) {
			pt = &card.Stats.Series[i]
		}
	}
	if pt == nil || pt.MBDown == nil {
		t.Fatalf("the pulsed hour is not in the series: %+v", card.Stats.Series)
	}
	// Deltas: first pulse 0, then +20 MB down, then the restart's whole 4 MB.
	if *pt.MBDown != 24 || *pt.MBUp != 3 {
		t.Fatalf("hour carried down %v up %v MB, want 24 and 3", *pt.MBDown, *pt.MBUp)
	}
	// 360 + 260 new connections over the hour.
	if want := round2(620.0 / 3600); *pt.ConnsPerSec != want {
		t.Fatalf("conns/s = %v, want %v", *pt.ConnsPerSec, want)
	}
	if *pt.CPUPct != 20 || *pt.MemMB != 340 {
		t.Fatalf("cpu %v mem %v, want mean 20 and peak 340", *pt.CPUPct, *pt.MemMB)
	}
	// Every other hour of the day is a gap, not a zero.
	for _, p := range card.Stats.Series {
		if p.Bucket != pt.Bucket && p.MBDown != nil {
			t.Fatalf("hour %s has a figure with no pulse in it", p.Bucket)
		}
	}
	r := card.Relays[0]
	if r.Version != "bbb222" || r.UptimeS == nil || *r.UptimeS != 60 || r.LastPulseAt == nil {
		t.Fatalf("last pulse = %+v", r)
	}
	if card.People == nil || *card.People != 2 {
		t.Fatalf("people through the box = %v, want 2", card.People)
	}
}
