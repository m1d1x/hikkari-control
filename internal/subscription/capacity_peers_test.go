package subscription

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

func TestCapacityAWGManagedPeers(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	a, b, out := "cap-a-"+suffix, "cap-b-"+suffix, "cap-out-"+suffix
	if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages(slot,in_russia) VALUES($1,true),($2,true),($3,false)`, a, b, out); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM entry_vantages WHERE slot=ANY($1)`, []string{a, b, out})
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	service := New(pool, &fakeBatch{}, []entry.Key{signer.Key()})
	first, err := service.capacityPeer(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.capacityPeer(ctx, a)
	if err != nil || first != again {
		t.Fatal("peer changed between jobs")
	}
	second, err := service.capacityPeer(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if first.awgPrivate == second.awgPrivate || first.awgAddress == second.awgAddress || first.awgAddress == entry.AwgProbeAddress {
		t.Fatal("points share a peer or reserved admission address")
	}
	if _, err := service.capacityPeer(ctx, out); err == nil {
		t.Fatal("foreign point received a peer")
	}
	rec := awgRecord()
	fleet := fakeFleet{records: [][]byte{signed(t, signer, rec)}}
	links, err := service.CapacityFleetLinks(ctx, fleet, a)
	if err != nil || len(links) != 1 {
		t.Fatalf("AWG fleet missing: %v %v", links, err)
	}
	expected, err := AwgLink(withKey(rec, first))
	if err != nil {
		t.Fatal(err)
	}
	if links[0] != expected {
		t.Fatal("fleet used admission/subscriber credential")
	}
	userLink, _ := AwgLink(withKey(rec, accessKey{awgPrivate: "subscriber", awgAddress: "10.66.0.200"}))
	if capacity.Key(links[0]) == "" || capacity.Key(links[0]) != capacity.Key(userLink) {
		t.Fatal("probe result cannot reach subscriber")
	}
	keys, err := service.KeysOf(ctx, rec.Name, entry.ProtoAmneziaWG)
	if err != nil {
		t.Fatal(err)
	}
	var public string
	if err := pool.QueryRow(ctx, `SELECT public_key FROM relay_capacity_awg_peers WHERE slot=$1`, a).Scan(&public); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, key := range keys {
		if key == public+" "+first.awgAddress {
			found = true
		}
		if strings.Contains(key, first.awgPrivate) {
			t.Fatal("private peer key sent to relay")
		}
	}
	if !found {
		t.Fatal("normal relay pulse missing public peer")
	}
	if _, err := pool.Exec(ctx, `UPDATE entry_vantages SET status='retired',retired_at=now() WHERE slot=$1`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CapacityFleetLinks(ctx, fleet, a); err == nil {
		t.Fatal("retired point received peer")
	}
	keys, err = service.KeysOf(ctx, rec.Name, entry.ProtoAmneziaWG)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if strings.HasPrefix(key, public+" ") {
			t.Fatal("retired peer stays authorized")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE entry_vantages SET status='active',retired_at=NULL,in_russia=false WHERE slot=$1`, a); err != nil {
		t.Fatal(err)
	}
	keys, err = service.KeysOf(ctx, rec.Name, entry.ProtoAmneziaWG)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if strings.HasPrefix(key, public+" ") {
			t.Fatal("foreign peer stays authorized")
		}
	}
}
