package entry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/rxcert"
)

// H5 of hikkari-client plans/2026-11-16-hidden-border-receiver-and-egress.md
// (the owner's decision of 16.11: the address the trunk reaches from Russia
// never shows in the open). A spare receiver on a box with the receiver role
// is granted to no account, a tester's included, so no subscription carries
// its address; the stand does not measure it and the node registry does not
// hold it. It takes the primary relay's keys instead (keys_from). A spare on a
// fleet box keeps its grants as before.
func TestAHiddenReceiverReachesNoSubscription(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := uuid.New()
	n := uint32(u[0])<<16 | uint32(u[1])<<8 | uint32(u[2])
	primary, hidden := fmt.Sprintf("rxhp%d", n), fmt.Sprintf("rxhh%d", n)
	domain := fmt.Sprintf("hid%d.lol", n)

	if err := h.svc.CreateBox(ctx, BoxSpec{Name: primary, Address: testAddr([6]byte(u[6:12]), 2), Provider: "test", Location: "amsterdam", Proto: ProtoHikkari, Domain: domain}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.CreateBox(ctx, BoxSpec{Name: hidden, Address: testAddr([6]byte(u[6:12]), 3), Provider: "test", Location: "amsterdam", Role: RoleReceiver}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO entry_domains (domain, tld, status, source, box) VALUES ($1, 'lol', 'assigned', 'purchased', $2)`, domain, primary); err != nil {
		t.Fatal(err)
	}
	prim, err := h.svc.AddRelay(ctx, primary, KindPersonal)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := h.svc.RelayIdentity(ctx, prim)
	if err != nil {
		t.Fatal(err)
	}
	hk := *pid.Hikkari
	hk.TrunkKey, hk.TrunkPorts, hk.TrunkPipeKey, hk.TrunkPipeWires = "aa", []int{10443}, "bb", map[string]int{"udp": 20447}
	pid.Hikkari = &hk
	if err := h.svc.setRelayIdentity(ctx, prim, pid); err != nil {
		t.Fatal(err)
	}
	h.admit(prim)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(3), DNSNames: []string{domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	kct, _ := cryptobox.Encrypt(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	if _, err := h.pool.Exec(ctx, `INSERT INTO entry_receiver_certs (domain, directory, chain_pem, key_ct, not_after) VALUES ($1, $2, $3, $4, $5)`,
		domain, rxcert.LetsEncrypt, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), kct, tpl.NotAfter); err != nil {
		t.Fatal(err)
	}

	job, err := h.svc.PrepareReceiverSpare(ctx, hidden, primary, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.finishJob(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	var spare string
	var from, primID int64
	if err := h.pool.QueryRow(ctx, `SELECT name, coalesce(keys_from, 0) FROM entry_relays WHERE box = $1`, hidden).Scan(&spare, &from); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `SELECT id FROM entry_relays WHERE name = $1`, prim).Scan(&primID); err != nil {
		t.Fatal(err)
	}
	if from != primID {
		t.Fatalf("the hidden spare takes the keys of relay %d, want the primary's %d", from, primID)
	}
	h.admit(spare) // what the provisioning job does once the spare passes its probe

	tester := testAccount(t, h.pool)
	if _, err := h.pool.Exec(ctx, `UPDATE hub_accounts SET all_relays = true WHERE id = $1`, tester); err != nil {
		t.Fatal(err)
	}
	got := h.batch(tester)
	if !slices.Contains(got, prim) {
		t.Fatalf("the tester's batch %v lacks the primary %s", got, prim)
	}
	if slices.Contains(got, spare) {
		t.Fatalf("the hidden receiver %s is in the tester's batch %v", spare, got)
	}
	if got := h.batch(testAccount(t, h.pool)); slices.Contains(got, spare) {
		t.Fatalf("the hidden receiver %s is in an ordinary batch %v", spare, got)
	}
	var grants int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id WHERE r.name = $1`, spare).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("grants on the hidden receiver: %d %v", grants, err)
	}

	records, err := h.svc.ServingRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range records {
		r, err := OpenRecord(raw, h.f.keys(t), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if r.Name == spare {
			t.Fatalf("the stand measures the hidden receiver %s", spare)
		}
	}

	if err := h.svc.syncFleetNodes(ctx); err != nil {
		t.Fatal(err)
	}
	var nodes int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_nodes n JOIN entry_relays r ON r.id = n.relay_id WHERE r.name = $1`, spare).Scan(&nodes); err != nil || nodes != 0 {
		t.Fatalf("nodes of the hidden receiver: %d %v", nodes, err)
	}
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_nodes n JOIN entry_relays r ON r.id = n.relay_id WHERE r.name = $1`, prim).Scan(&nodes); err != nil || nodes != 1 {
		t.Fatalf("nodes of the primary: %d %v", nodes, err)
	}
}

// G2 of hikkari-client plans/2026-11-19-border-forwarders.md (the owner's
// decision of 20.11): the egress becomes the core of the group, the primary's
// keys and certificate on ports of its own, granted to no account. A spare
// on an egress is still refused, and a core only goes on an egress.
func TestTheCoreOnAnEgressTakesThePrimarysKeysOnItsOwnPorts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := uuid.New()
	n := uint32(u[0])<<16 | uint32(u[1])<<8 | uint32(u[2])
	primary, eg, other := fmt.Sprintf("corep%d", n), fmt.Sprintf("coree%d", n), fmt.Sprintf("corer%d", n)
	domain := fmt.Sprintf("core%d.lol", n)
	for i, b := range []BoxSpec{
		{Name: primary, Provider: "test", Location: "amsterdam", Proto: ProtoHikkari, Domain: domain},
		{Name: eg, Provider: "test", Location: "amsterdam", Role: RoleEgress},
		{Name: other, Provider: "test", Location: "amsterdam", Role: RoleReceiver},
	} {
		b.Address = testAddr([6]byte(u[6:12]), uint16(2+i))
		if err := h.svc.CreateBox(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO entry_domains (domain, tld, status, source, box) VALUES ($1, 'lol', 'assigned', 'purchased', $2)`, domain, primary); err != nil {
		t.Fatal(err)
	}
	prim, err := h.svc.AddRelay(ctx, primary, KindPersonal)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := h.svc.RelayIdentity(ctx, prim)
	if err != nil {
		t.Fatal(err)
	}
	hk := *pid.Hikkari
	hk.TrunkKey, hk.TrunkPorts, hk.TrunkPipeKey, hk.TrunkPipeWires = "aa", []int{pid.Port, 2053}, "bb", map[string]int{"udp": 20461, "h3dgram": 20460}
	pid.Hikkari = &hk
	if err := h.svc.setRelayIdentity(ctx, prim, pid); err != nil {
		t.Fatal(err)
	}
	h.admit(prim)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(4), DNSNames: []string{domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	kct, _ := cryptobox.Encrypt(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	if _, err := h.pool.Exec(ctx, `INSERT INTO entry_receiver_certs (domain, directory, chain_pem, key_ct, not_after) VALUES ($1, $2, $3, $4, $5)`,
		domain, rxcert.LetsEncrypt, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), kct, tpl.NotAfter); err != nil {
		t.Fatal(err)
	}

	if _, err := h.svc.PrepareReceiverSpare(ctx, eg, primary, false, ""); err == nil {
		t.Fatal("a spare goes on an egress")
	}
	if _, err := h.svc.PrepareCore(ctx, other, primary, 8443, ""); err == nil {
		t.Fatal("a core goes on a receiver")
	}
	job, err := h.svc.PrepareCore(ctx, eg, primary, pid.Port+8000, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.finishJob(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	var core string
	var from, primID int64
	if err := h.pool.QueryRow(ctx, `SELECT name, coalesce(keys_from, 0) FROM entry_relays WHERE box = $1`, eg).Scan(&core, &from); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `SELECT id FROM entry_relays WHERE name = $1`, prim).Scan(&primID); err != nil {
		t.Fatal(err)
	}
	if from != primID {
		t.Fatalf("the core takes the keys of relay %d, want the primary's %d", from, primID)
	}
	cid, err := h.svc.RelayIdentity(ctx, core)
	if err != nil {
		t.Fatal(err)
	}
	ch := cid.Hikkari
	if cid.Port != pid.Port+8000 || ch.TrunkPipeKey != "bb" || ch.Host != domain || ch.TrunkKey != "" ||
		ch.TrunkPipeWires["udp"] != 28461 || ch.TrunkPipeWires["h3dgram"] != 28460 {
		t.Fatalf("core identity port %d %+v", cid.Port, ch)
	}
	h.admit(core)
	tester := testAccount(t, h.pool)
	if _, err := h.pool.Exec(ctx, `UPDATE hub_accounts SET all_relays = true WHERE id = $1`, tester); err != nil {
		t.Fatal(err)
	}
	if got := h.batch(tester); slices.Contains(got, core) {
		t.Fatalf("the core %s is in the tester's batch %v", core, got)
	}
}
