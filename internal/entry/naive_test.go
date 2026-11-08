package entry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestNaiveIdentityRecordAndEnv(t *testing.T) {
	id, err := newNaiveIdentity(443, 10001, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if id.proto() != ProtoNaive || id.echoPort() != 0 || id.Naive.H3 {
		t.Fatal("wrong Naive defaults")
	}
	env, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	if env["EDGE_PROTO"] != ProtoNaive || env["EDGE_NAIVE_PASS"] != id.Naive.Pass {
		t.Fatal("lost edge credentials")
	}
	record := id.record("nvn1-1", "203.0.113.1", "", 1, 100)
	if err := record.check(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RelayRecord
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.NaiveParams == nil || decoded.NaiveParams.Pass != id.Naive.Pass {
		t.Fatal("lost signed Naive parameters")
	}
	record.NaiveParams.Host = "127.0.0.1"
	if record.check() == nil {
		t.Fatal("accepted an IP as certificate domain")
	}
}

func TestHikkariIdentityRecordAndEnv(t *testing.T) {
	id, err := newHikkariIdentity(443, 10001, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if id.proto() != ProtoHikkari || id.echoPort() != 0 || id.Naive != nil || id.caddyHost() != "node.example.com" {
		t.Fatal("wrong Hikkari identity")
	}
	env, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	if env["EDGE_PROTO"] != ProtoHikkari || env["EDGE_HIKKARI_PASS"] != id.Hikkari.Pass || env["EDGE_HIKKARI_HOST"] != "node.example.com" {
		t.Fatal("lost edge credentials")
	}
	if _, naive := env["EDGE_NAIVE_PASS"]; naive {
		t.Fatal("a Hikkari edge got Naive's env")
	}
	record := id.record("hk1-1", "203.0.113.1", "", 1, 100)
	if err := record.check(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RelayRecord
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Proto != ProtoHikkari || decoded.HikkariParams == nil || decoded.HikkariParams.Pass != id.Hikkari.Pass || decoded.NaiveParams != nil {
		t.Fatal("lost signed Hikkari parameters")
	}
	moved, err := id.withDomain("other.example.com")
	if err != nil || moved.Hikkari.Host != "other.example.com" || id.Hikkari.Host != "node.example.com" {
		t.Fatal("domain move")
	}
	if !domainProtocol(ProtoHikkari) {
		t.Fatal("a Hikkari relay lives on a domain")
	}
	record.HikkariParams.Host = "127.0.0.1"
	if record.check() == nil {
		t.Fatal("accepted an IP as certificate domain")
	}
	both := id.record("hk1-1", "203.0.113.1", "", 1, 100)
	both.NaiveParams = &NaiveParams{Host: "node.example.com", User: "probe", Pass: id.Hikkari.Pass}
	if both.check() == nil {
		t.Fatal("accepted a record with two halves")
	}
}

func TestNaiveAdoptionIsAtomicAndRefusesServingMachines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, err := NewService(pool, nil, DefaultBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	box := "nvadopt901"
	if err = s.CreateBox(ctx, BoxSpec{Name: box, Address: "203.0.113.91", Provider: "test", Location: "eu"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.prepareAdoption(ctx, box, ProtoNaive, "node.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	var proto string
	if err = pool.QueryRow(ctx, `SELECT proto FROM entry_boxes WHERE name=$1`, box).Scan(&proto); err != nil || proto != ProtoNaive {
		t.Fatalf("not adopted: %s %v", proto, err)
	}
	if _, err = s.prepareAdoption(ctx, box, ProtoNaive, "other.example.com", ""); !errors.Is(err, ErrBoxBusy) {
		t.Fatalf("concurrent job accepted: %v", err)
	}
	if err = s.finishJob(ctx, id, errors.New("test finishes job")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddRelay(ctx, box, KindPersonal); err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareAdoption(ctx, box, ProtoNaive, "other.example.com", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("pending identity changed domain: %v", err)
	}
	retryID, err := s.prepareAdoption(ctx, box, ProtoNaive, "node.example.com", "")
	if err != nil {
		t.Fatalf("failed adoption cannot resume: %v", err)
	}
	if err = s.finishJob(ctx, retryID, errors.New("finish retry")); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE entry_relays SET status='alive',envelope_ct=decode('01','hex'),key_id='test',issued_at=now(),expires_at=now()+interval '1 day',signed_at=now() WHERE box=$1`, box); err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareAdoption(ctx, box, ProtoNaive, "node.example.com", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("serving relay converted: %v", err)
	}
}

func TestHikkariAdoptionClaimsTheBoxAsHikkari(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, err := NewService(pool, nil, DefaultBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	box := "hkadopt902"
	if err = s.CreateBox(ctx, BoxSpec{Name: box, Address: "203.0.113.92", Provider: "test", Location: "eu"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareAdoption(ctx, box, ProtoVlessXhttp, "node.example.com", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("adopted as a protocol Caddy does not serve: %v", err)
	}
	id, err := s.prepareAdoption(ctx, box, ProtoHikkari, "node.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	var proto string
	if err = pool.QueryRow(ctx, `SELECT proto FROM entry_boxes WHERE name=$1`, box).Scan(&proto); err != nil || proto != ProtoHikkari {
		t.Fatalf("not adopted as hikkari: %s %v", proto, err)
	}
	if err = s.finishJob(ctx, id, errors.New("test finishes job")); err != nil {
		t.Fatal(err)
	}
	rel, err := s.AddRelay(ctx, box, KindPersonal)
	if err != nil {
		t.Fatal(err)
	}
	var idCT []byte
	if err = pool.QueryRow(ctx, `SELECT identity_ct FROM entry_relays WHERE name=$1`, rel).Scan(&idCT); err != nil {
		t.Fatal(err)
	}
	opened, err := openIdentity(idCT)
	if err != nil || opened.proto() != ProtoHikkari || opened.Naive != nil || opened.Hikkari.Host != "node.example.com" {
		t.Fatalf("relay of an adopted box is not Hikkari: %v", err)
	}
}
