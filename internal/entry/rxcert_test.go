package entry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/rxcert"
)

// The group's certificate goes into a Hikkari relay's env only while it is
// valid; only a domain the hub bought is proved; the ACME account key is
// made once and kept.
func TestTheReceiverGroupCertificate(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	ctx := context.Background()
	s, err := NewService(pool, nil, DefaultBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM entry_receiver_certs`, `DELETE FROM entry_acme_accounts`, `DELETE FROM entry_domains WHERE domain IN ('otter9119.lol', 'seeded4242.lol')`} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_domains (domain, tld, status, source) VALUES
		('otter9119.lol', 'lol', 'assigned', 'purchased'), ('seeded4242.lol', 'lol', 'owned', 'seeded')`); err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]string{"otter9119.lol": "otter9119.lol", "rx.otter9119.lol": "otter9119.lol"} {
		if zone, err := s.receiverZone(ctx, domain); err != nil || zone != want {
			t.Fatalf("%s: zone %q %v", domain, zone, err)
		}
	}
	for _, domain := range []string{"seeded4242.lol", "xotter9119.lol", "pine1.lol"} {
		if _, err := s.receiverZone(ctx, domain); !errors.Is(err, ErrRejected) {
			t.Fatalf("%s proved: %v", domain, err)
		}
	}

	if env, err := s.receiverCertEnv(ctx, "otter9119.lol"); err != nil || env != nil {
		t.Fatalf("no certificate kept, yet env %v %v", env, err)
	}
	keep := func(notAfter time.Time) string {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"otter9119.lol"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
		der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
		kder, _ := x509.MarshalECPrivateKey(key)
		chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		kct, _ := cryptobox.Encrypt(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
		if _, err := pool.Exec(ctx, `
			INSERT INTO entry_receiver_certs (domain, directory, chain_pem, key_ct, not_after) VALUES ('otter9119.lol', $1, $2, $3, $4)
			ON CONFLICT (domain) DO UPDATE SET chain_pem = $2, key_ct = $3, not_after = $4`, rxcert.LetsEncrypt, chain, kct, notAfter); err != nil {
			t.Fatal(err)
		}
		return chain
	}
	chain := keep(time.Now().Add(60 * 24 * time.Hour))
	env, err := s.receiverCertEnv(ctx, "OTTER9119.lol")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := base64.StdEncoding.DecodeString(env["EDGE_HIKKARI_CERT"])
	key, _ := base64.StdEncoding.DecodeString(env["EDGE_HIKKARI_KEY"])
	if string(got) != chain || len(key) == 0 {
		t.Fatalf("env %v", env)
	}
	keep(time.Now().Add(-time.Minute))
	if env, err := s.receiverCertEnv(ctx, "otter9119.lol"); err != nil || env != nil {
		t.Fatalf("an expired certificate went into the env: %v %v", env, err)
	}

	k1, err := s.acmeAccountKey(ctx, rxcert.LetsEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := s.acmeAccountKey(ctx, rxcert.LetsEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	if !k1.Public().(*ecdsa.PublicKey).Equal(k2.Public()) {
		t.Fatal("the account key was not kept")
	}
}

// A spare receiver (P2) takes the primary's relay whole, only once the group
// has the hub's certificate and only over live relays the owner said to
// replace; the domain's A record stays the primary's.
func TestASpareReceiverIsThePrimaryAgain(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	ctx := context.Background()
	s, err := NewService(pool, nil, DefaultBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	primary, spare := "rxprim903", "rxspare904"
	domain := "reef9043.lol"
	for _, q := range []string{`DELETE FROM entry_receiver_certs WHERE domain = 'reef9043.lol'`, `DELETE FROM entry_domains WHERE domain = 'reef9043.lol'`} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CreateBox(ctx, BoxSpec{Name: primary, Address: "203.0.113.93", Provider: "test", Location: "eu", Proto: ProtoHikkari, Domain: domain}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateBox(ctx, BoxSpec{Name: spare, Address: "203.0.113.94", Provider: "test", Location: "eu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO entry_domains (domain, tld, status, source, box) VALUES ($1, 'lol', 'assigned', 'purchased', $2)`, domain, primary); err != nil {
		t.Fatal(err)
	}
	prim, err := s.AddRelay(ctx, primary, KindPersonal)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := s.RelayIdentity(ctx, prim)
	if err != nil {
		t.Fatal(err)
	}
	h := *pid.Hikkari
	h.TrunkKey, h.TrunkPorts, h.TrunkPipeKey, h.TrunkPipeWires = "aa", []int{10443}, "bb", map[string]int{"udp": 20447}
	pid.Hikkari = &h
	if err := s.setRelayIdentity(ctx, prim, pid); err != nil {
		t.Fatal(err)
	}
	old, err := s.AddRelay(ctx, spare, KindPersonal) // a Reality relay
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE entry_relays SET status='alive',envelope_ct=decode('01','hex'),key_id='test',issued_at=now(),expires_at=now()+interval '1 day',signed_at=now() WHERE name=$1`, old); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PrepareReceiverSpare(ctx, spare, primary, true, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("a spare without the group's certificate: %v", err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	kct, _ := cryptobox.Encrypt(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	if _, err := pool.Exec(ctx, `INSERT INTO entry_receiver_certs (domain, directory, chain_pem, key_ct, not_after) VALUES ($1, $2, $3, $4, $5)`,
		domain, rxcert.LetsEncrypt, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), kct, tpl.NotAfter); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareReceiverSpare(ctx, spare, primary, false, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("live relays replaced without the word: %v", err)
	}
	job, err := s.PrepareReceiverSpare(ctx, spare, primary, true, "")
	if err != nil {
		t.Fatal(err)
	}
	var status, proto string
	if err := pool.QueryRow(ctx, `SELECT status FROM entry_relays WHERE name = $1`, old).Scan(&status); err != nil || status != "dead" {
		t.Fatalf("the replaced relay: %s %v", status, err)
	}
	if err := pool.QueryRow(ctx, `SELECT proto FROM entry_boxes WHERE name = $1`, spare).Scan(&proto); err != nil || proto != ProtoHikkari {
		t.Fatalf("the spare's protocol: %s %v", proto, err)
	}
	got, err := s.receiverOf(ctx, spare)
	if err != nil {
		t.Fatal(err)
	}
	if g := got.id.Hikkari; got.id.Port != pid.Port || g.Host != domain || g.Pass != h.Pass || g.TrunkKey != "aa" || g.TrunkPipeKey != "bb" || g.TrunkPipeWires["udp"] != 20447 {
		t.Fatalf("the spare is not the primary again: %+v", got.id)
	}
	if holder, err := s.groupDomainHolder(ctx, spare, domain); err != nil || holder != primary {
		t.Fatalf("the A record's box seen from the spare: %q %v", holder, err)
	}
	if holder, err := s.groupDomainHolder(ctx, primary, domain); err != nil || holder != "" {
		t.Fatalf("the primary read as a spare: %q %v", holder, err)
	}
	if boxes, err := s.ReceiverBoxes(ctx, domain); err != nil || len(boxes) != 2 {
		t.Fatalf("the group: %v %v", boxes, err)
	}
	if err := s.finishJob(ctx, job, errors.New("test finishes job")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareReceiverSpare(ctx, spare, primary, true, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("a spare made twice: %v", err)
	}
}
