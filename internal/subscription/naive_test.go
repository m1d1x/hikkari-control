package subscription

import (
	"context"
	"github.com/google/uuid"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNaiveSubscriberLinkPreservesPersonalKeyAndNode(t *testing.T) {
	record := entry.RelayRecord{Name: "nvn1-1", Address: "203.0.113.1", Port: 443, Proto: entry.ProtoNaive, NaiveParams: &entry.NaiveParams{Host: "node.example.com", User: "probe", Pass: "probe-secret"}}
	personal := withKey(record, accessKey{hy2Auth: "personal-secret"})
	if record.NaiveParams.Pass != "probe-secret" {
		t.Fatal("mutated probe record")
	}
	link, err := Link(personal)
	if err != nil {
		t.Fatal(err)
	}
	link = namedLink(link, entry.NodeName{Slug: "nvn1", Label: "Naive"}, nil)
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := u.User.Password()
	if u.Scheme != "naive+https" || u.User.Username() != "hikkari" || pass != "personal-secret" || u.Query().Get("sni") != "node.example.com" || u.Query().Get("hikkari-node") != "n:nvn1" {
		t.Fatal("wrong subscriber link")
	}
	h := http.Header{}
	if hasNaiveFeature(h) {
		t.Fatal("old client opted into Naive")
	}
	h.Set(featuresHeader, "amneziawg, naive")
	if !hasNaiveFeature(h) {
		t.Fatal("missing Naive capability")
	}
}

func TestNaiveRosterEnforcesSubscriptionTrafficLimit(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	s := New(pool, &fakeBatch{}, nil)
	name := "naive-" + uuid.NewString()[:8]
	if _, err := s.Add(ctx, name, unlimited(t, s)); err != nil {
		t.Fatal(err)
	}
	key := keyOf(t, pool, name)
	relay, _ := grantRelay(t, pool, entry.ProtoNaive, name)
	keys, err := s.KeysOf(ctx, relay, entry.ProtoNaive)
	if err != nil || len(keys) != 1 || keys[0] != key.hy2Auth {
		t.Fatalf("wrong Naive credential roster: %v %v", keys, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE hub_subscriptions SET traffic_bytes=1 WHERE account_id=(SELECT id FROM hub_accounts WHERE name=$1)`, name); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordUsage(ctx, relay, strings.Repeat("a", 32), map[string]entry.KeyUsage{key.hy2Auth: {Up: 10, Down: 100}}); err != nil {
		t.Fatal(err)
	}
	if keys, err = s.KeysOf(ctx, relay, entry.ProtoNaive); err != nil || len(keys) != 0 {
		t.Fatalf("spent subscription kept access: %v %v", keys, err)
	}
}
