package entry

import (
	"context"
	"testing"
	"time"
)

type nodeSignalFixture struct {
	verdict string
	offset  time.Duration
}

func (f *nodeSignalFixture) FleetSignals(_ context.Context, nodes []FleetNode, now time.Time) (map[string]NodeSignal, error) {
	out := map[string]NodeSignal{}
	for _, n := range nodes {
		out[n.Relay] = NodeSignal{ID: "n:" + n.Slug, Verdict: f.verdict, At: now.Add(f.offset)}
	}
	return out, nil
}
func TestNodeSlugSurvivesReplacementWithinItsPool(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Make the slot choice independent of other tests' historical dead relays.
	if _, err := h.pool.Exec(ctx, `DELETE FROM entry_nodes`); err != nil {
		t.Fatal(err)
	}
	box, _ := h.box()
	old := h.relay(box, KindPersonal)
	h.admit(old)
	before, err := h.svc.NodeSlugs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before[old] == "" {
		t.Fatal("admission has no logical node")
	}
	if _, _, err := h.svc.Retire(ctx, box, DeathOther); err != nil {
		t.Fatal(err)
	}
	freshBox, _ := h.box()
	fresh := h.relay(freshBox, KindPersonal)
	h.admit(fresh)
	after, err := h.svc.NodeSlugs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after[fresh] != before[old] {
		t.Fatalf("replacement changed slug: %v -> %v", before, after)
	}
	nodes, err := h.svc.FleetNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Slug == after[fresh] && (n.Relay != fresh || n.Box != freshBox || n.Host == "") {
			t.Fatalf("stale registry binding: %+v", n)
		}
	}
}
func TestDowndetectorSignalsOpenDeduplicateAndRecoverIncidents(t *testing.T) {
	for _, tc := range []struct{ verdict, cause string }{{"server_down", CauseIPBanned}, {"isp_block", CauseFingerprint}, {"country_block", CauseFingerprint}} {
		t.Run(tc.verdict, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			box, _ := h.box()
			relay := h.relay(box, KindPersonal)
			h.admit(relay)
			src := &nodeSignalFixture{verdict: tc.verdict}
			h.svc.SetNodeSignals(src)
			for range 2 {
				if _, _, err := h.svc.Detect(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_incidents WHERE box=$1 AND cause=$2 AND status='open'`, box, tc.cause).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("incidents=%d, want 1", count)
			}
			src.verdict = "unknown"
			if _, _, err := h.svc.Detect(ctx); err != nil {
				t.Fatal(err)
			}
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_incidents WHERE box=$1 AND cause=$2 AND status='open'`, box, tc.cause).Scan(&count); err != nil || count != 1 {
				t.Fatalf("missing evidence closed incident: %d %v", count, err)
			}
			src.verdict = "healthy"
			if _, _, err := h.svc.Detect(ctx); err != nil {
				t.Fatal(err)
			}
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_incidents WHERE box=$1 AND cause=$2 AND status='open'`, box, tc.cause).Scan(&count); err != nil || count != 0 {
				t.Fatalf("recovery kept incident: %d %v", count, err)
			}
		})
	}
}
func TestStaleNodeSignalsCannotTriggerRepair(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)
	h.svc.SetNodeSignals(&nodeSignalFixture{verdict: "country_block", offset: -time.Hour})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_incidents WHERE box=$1 AND cause='fingerprint'`, box).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale signal acted: %d %v", count, err)
	}
}
