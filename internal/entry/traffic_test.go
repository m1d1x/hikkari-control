package entry

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// trafficRecorder keeps what RecordUserTraffic handed the dataset.
type trafficRecorder struct {
	rows []UserTrafficRecord
}

func (r *trafficRecorder) InsertEntryUserTraffic(_ context.Context, rows []UserTrafficRecord) error {
	r.rows = append(r.rows, rows...)
	return nil
}

// A socket whose peer is a box came through that box, read off the hub's own
// sealed record; any other peer is direct. Neither the account nor the
// address reaches the dataset — the account as the reach pseudonym, the
// address not at all.
func TestUserTrafficIsAttributedToTheBoxItCameThroughAndKeepsNoIdentity(t *testing.T) {
	h := newHarness(t)
	box, addr := h.box()
	rec := &trafficRecorder{}
	h.svc.SetUserTrafficSink(rec)

	account := uuid.New()
	if err := h.svc.RecordUserTraffic(context.Background(), []UserTrafficReport{
		{Account: account, PeerIP: addr, BytesUp: 1200, BytesDown: 64000},
		{Account: account, PeerIP: "198.51.100.77", BytesUp: 10, BytesDown: 20},
		// Carried nothing: not a row.
		{Account: account, PeerIP: addr},
	}); err != nil {
		t.Fatalf("record traffic: %v", err)
	}
	if len(rec.rows) != 2 {
		t.Fatalf("sink took %d rows, want 2: %+v", len(rec.rows), rec.rows)
	}
	via, direct := rec.rows[0], rec.rows[1]
	if via.Box != box || via.BytesUp != 1200 || via.BytesDown != 64000 {
		t.Fatalf("relay socket stored as %+v, want box %s", via, box)
	}
	if direct.Box != "" {
		t.Fatalf("a direct socket was attributed to box %q", direct.Box)
	}
	if via.Account != h.svc.reachPseudonym(account) || via.Account == account.String() {
		t.Fatalf("account stored as %q: must be the reach pseudonym", via.Account)
	}
	if len(via.Account) != 32 {
		t.Fatalf("pseudonym is %d chars, the dataset column is FixedString(32)", len(via.Account))
	}
}

// A retired machine's address goes back to the hoster and may be someone
// else's tomorrow: its sockets are no longer that box's.
func TestUserTrafficForgetsARetiredBox(t *testing.T) {
	h := newHarness(t)
	box, addr := h.box()
	rec := &trafficRecorder{}
	h.svc.SetUserTrafficSink(rec)
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE entry_boxes SET retired_at = now(), retired_reason = 'rotated' WHERE name = $1`, box); err != nil {
		t.Fatalf("retire: %v", err)
	}

	if err := h.svc.RecordUserTraffic(context.Background(), []UserTrafficReport{
		{Account: uuid.New(), PeerIP: addr, BytesDown: 5},
	}); err != nil {
		t.Fatalf("record traffic: %v", err)
	}
	if len(rec.rows) != 1 || rec.rows[0].Box != "" {
		t.Fatalf("rows = %+v, want one direct row", rec.rows)
	}
}

func TestCanonicalIPFoldsMappedIPv4(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":        "203.0.113.9",
		"::ffff:203.0.113.9": "203.0.113.9",
		"2001:DB8::1":        "2001:db8::1",
		"not an ip":          "",
	} {
		if got := canonicalIP(in); got != want {
			t.Errorf("canonicalIP(%q) = %q, want %q", in, got, want)
		}
	}
}
