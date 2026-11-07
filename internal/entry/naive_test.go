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
	id, err := s.prepareNaiveAdoption(ctx, box, "node.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	var proto string
	if err = pool.QueryRow(ctx, `SELECT proto FROM entry_boxes WHERE name=$1`, box).Scan(&proto); err != nil || proto != ProtoNaive {
		t.Fatalf("not adopted: %s %v", proto, err)
	}
	if _, err = s.prepareNaiveAdoption(ctx, box, "other.example.com", ""); !errors.Is(err, ErrBoxBusy) {
		t.Fatalf("concurrent job accepted: %v", err)
	}
	if err = s.finishJob(ctx, id, errors.New("test finishes job")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddRelay(ctx, box, KindPersonal); err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareNaiveAdoption(ctx, box, "other.example.com", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("pending identity changed domain: %v", err)
	}
	retryID, err := s.prepareNaiveAdoption(ctx, box, "node.example.com", "")
	if err != nil {
		t.Fatalf("failed adoption cannot resume: %v", err)
	}
	if err = s.finishJob(ctx, retryID, errors.New("finish retry")); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE entry_relays SET status='alive',envelope_ct=decode('01','hex'),key_id='test',issued_at=now(),expires_at=now()+interval '1 day',signed_at=now() WHERE box=$1`, box); err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareNaiveAdoption(ctx, box, "node.example.com", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("serving relay converted: %v", err)
	}
}
