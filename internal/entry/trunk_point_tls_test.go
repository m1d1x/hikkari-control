package entry

import (
	"errors"
	"testing"
	"time"
)

// TestAFreshEntryTakesItsPointsNamesOnAStrictCore: a core that answers its
// points' names only (allow_legacy off) brings no member of an entry up
// that dials them without names (25.11, the second entry cbxmsk1 on
// aeznl1). An entry the operator has not chosen for gets the names of all
// its points, on record, so the core's names are not cleared under it; a
// core that still takes the old name, or an operator's choice, is left as
// it is; a point without a name on a strict core is refused.
func TestAFreshEntryTakesItsPointsNamesOnAStrictCore(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	s := &Service{pool: pool}
	ctx := t.Context()
	for _, box := range []string{"tlsentry1", "tlsentry2", "tlscore1", "tlspoint1", "tlspoint2", "tlspoint3"} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, '\x00', 'test', 'test', 'hikkari') ON CONFLICT (name) DO NOTHING`, box); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`DELETE FROM entry_trunk_point_tls_uses WHERE entry_box IN ('tlsentry1', 'tlsentry2')`,
		`DELETE FROM entry_trunk_point_tls WHERE core_box = 'tlscore1'`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"edge.tlspoint1.test", "edge.tlspoint2.test"} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_receiver_certs (domain, directory, chain_pem, key_ct, not_after)
			VALUES ($1, 'test', 'test', '\x00', $2) ON CONFLICT (domain) DO NOTHING`, d, time.Now().Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []string{"tlsentry1", "tlsentry2"} {
		if err := s.recordTrunkEntry(ctx, e, TrunkEntryOptions{Exit: "tlscore1", Members: 32, Forward: map[int]int{8443: 8443}}); err != nil {
			t.Fatal(err)
		}
	}
	points := []string{"tlspoint1", "tlspoint2"}
	uses := func(box string) []string {
		var got []string
		err := pool.QueryRow(ctx, `SELECT points FROM entry_trunk_point_tls_uses WHERE entry_box = $1 AND NOT stand`, box).Scan(&got)
		if err != nil {
			return nil
		}
		return got
	}
	if err := s.namePointsOnStrictCore(ctx, "tlsentry1", "tlscore1", false, points); err != nil || uses("tlsentry1") != nil {
		t.Fatalf("a core with no names of its points: %v, uses %v", err, uses("tlsentry1"))
	}
	names := map[string]string{"tlspoint1": "edge.tlspoint1.test", "tlspoint2": "edge.tlspoint2.test"}
	if err := s.writeCorePointTLS(ctx, "tlscore1", 8443, true, names); err != nil {
		t.Fatal(err)
	}
	if err := s.namePointsOnStrictCore(ctx, "tlsentry1", "tlscore1", false, points); err != nil || uses("tlsentry1") != nil {
		t.Fatalf("a core that still takes the old name: %v, uses %v", err, uses("tlsentry1"))
	}
	if err := s.writeCorePointTLS(ctx, "tlscore1", 8443, false, names); err != nil {
		t.Fatal(err)
	}
	if err := s.namePointsOnStrictCore(ctx, "tlsentry1", "tlscore1", false, points); err != nil {
		t.Fatal(err)
	}
	if got := uses("tlsentry1"); len(got) != 2 || got[0] != "tlspoint1" || got[1] != "tlspoint2" {
		t.Fatalf("a fresh entry on a strict core uses %v", got)
	}
	got, err := s.pointSNI(ctx, "tlsentry1", "tlscore1", false, points, []string{"192.0.2.1", "192.0.2.2"})
	if err != nil || got["192.0.2.1"] != "edge.tlspoint1.test" || got["192.0.2.2"] != "edge.tlspoint2.test" {
		t.Fatalf("names in the wires %v %v", got, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE entry_trunk_point_tls_uses SET points = '{tlspoint1}' WHERE entry_box = 'tlsentry1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.namePointsOnStrictCore(ctx, "tlsentry1", "tlscore1", false, points); err != nil {
		t.Fatal(err)
	}
	if got := uses("tlsentry1"); len(got) != 1 {
		t.Fatalf("the operator's choice was overwritten: %v", got)
	}
	err = s.namePointsOnStrictCore(ctx, "tlsentry2", "tlscore1", false, []string{"tlspoint1", "tlspoint3"})
	if !errors.Is(err, ErrRejected) || uses("tlsentry2") != nil {
		t.Fatalf("a point with no name on a strict core: %v, uses %v", err, uses("tlsentry2"))
	}
	// The entry's stand (trunk-pipe, the country gateways' NL backend) dials
	// the same points with a row of its own: 25.11 the stand on cbxmsk1 came
	// up without names while its entry had them.
	if err := s.namePointsOnStrictCore(ctx, "tlsentry2", "tlscore1", true, points); err != nil {
		t.Fatal(err)
	}
	var stand []string
	if err := pool.QueryRow(ctx, `SELECT points FROM entry_trunk_point_tls_uses WHERE entry_box = 'tlsentry2' AND stand`).Scan(&stand); err != nil || len(stand) != 2 {
		t.Fatalf("a fresh stand on a strict core uses %v: %v", stand, err)
	}
	if uses("tlsentry2") != nil {
		t.Fatalf("the stand's names went to its entry: %v", uses("tlsentry2"))
	}
	got, err = s.pointSNI(ctx, "tlsentry2", "tlscore1", true, points, []string{"192.0.2.1", "192.0.2.2"})
	if err != nil || got["192.0.2.1"] != "edge.tlspoint1.test" || got["192.0.2.2"] != "edge.tlspoint2.test" {
		t.Fatalf("names in the stand's wires %v %v", got, err)
	}
}
