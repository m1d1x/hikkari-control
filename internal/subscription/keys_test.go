package subscription

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// grantRelay puts a relay of proto on a fresh box and grants it to name.
func grantRelay(t *testing.T, pool db.Pool, proto string, names ...string) (relay string, grants []int64) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	box := "kt" + strings.Map(func(r rune) rune {
		if r >= 'a' {
			return -1
		}
		return r
	}, suffix) + "1"
	var region any
	if proto == entry.ProtoHysteria2 {
		region = "eu"
	}
	if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto, region) VALUES ($1, '\x00', 'test', 'de', $2, $3)`, box, proto, region); err != nil {
		t.Fatal(err)
	}
	relay = box + "-r"
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO entry_relays (name, kind, box, identity_ct) VALUES ($1, 'personal', $2, '\x00') RETURNING id`, relay, box).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		var g int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO entry_grants (account_id, relay_id, trigger)
			SELECT id, $2, 'initial' FROM hub_accounts WHERE name = $1 RETURNING id`, n, id).Scan(&g); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, g)
	}
	return relay, grants
}

func keyOf(t *testing.T, pool db.Pool, name string) accessKey {
	t.Helper()
	var k accessKey
	if err := pool.QueryRow(context.Background(), `
		SELECT k.vless_id::text, k.hy2_auth FROM hub_access_keys k JOIN hub_accounts a ON a.id = k.account_id
		WHERE a.name = $1`, name).Scan(&k.vlessID, &k.hy2Auth); err != nil {
		t.Fatal(err)
	}
	return k
}

// A relay learns exactly the keys of live subscriptions granted to it, in its
// protocol's credential; a revoked subscription or grant leaves the list.
func TestARelayLearnsTheKeysOfItsLiveGrants(t *testing.T) {
	pool := livePool(t)
	s := New(pool, &fakeBatch{}, nil)
	ctx := context.Background()
	a, b := "t-"+uuid.NewString()[:8], "t-"+uuid.NewString()[:8]
	for _, n := range []string{a, b} {
		if _, err := s.Add(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	ka, kb := keyOf(t, pool, a), keyOf(t, pool, b)
	if ka.vlessID == kb.vlessID || ka.hy2Auth == kb.hy2Auth {
		t.Fatal("two accounts share a key")
	}

	stream, grants := grantRelay(t, pool, entry.ProtoVlessXhttp, a, b)
	voice, _ := grantRelay(t, pool, entry.ProtoHysteria2, a)

	keys, err := s.KeysOf(ctx, stream, entry.ProtoVlessXhttp)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ka.vlessID, kb.vlessID}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("stream keys %v, want %v", keys, want)
	}
	if keys, _ := s.KeysOf(ctx, voice, entry.ProtoHysteria2); !slices.Equal(keys, []string{ka.hy2Auth}) {
		t.Fatalf("voice keys %v", keys)
	}

	if err := s.Revoke(ctx, a); err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.KeysOf(ctx, stream, entry.ProtoVlessXhttp); !slices.Equal(keys, []string{kb.vlessID}) {
		t.Fatalf("after revoking a subscription: %v", keys)
	}
	if _, err := pool.Exec(ctx, `UPDATE entry_grants SET revoked_at = now(), revoke_reason = 'drained' WHERE id = $1`, grants[1]); err != nil {
		t.Fatal(err)
	}
	if keys, err := s.KeysOf(ctx, stream, entry.ProtoVlessXhttp); err != nil || keys == nil || len(keys) != 0 {
		t.Fatalf("after revoking the grant: %v %v (an empty list, not none)", keys, err)
	}
}

// Usage adds up across relays and edge runs; within one run a late pulse
// never rolls a total back; a key the hub does not know counts for nobody.
func TestUsageIsSummedPerSubscription(t *testing.T) {
	pool := livePool(t)
	s := New(pool, &fakeBatch{}, nil)
	ctx := context.Background()
	name := "t-" + uuid.NewString()[:8]
	if _, err := s.Add(ctx, name); err != nil {
		t.Fatal(err)
	}
	k := keyOf(t, pool, name)
	boot1, boot2 := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, step := range []struct {
		relay, boot, key string
		u                entry.KeyUsage
	}{
		{"r1", boot1, k.vlessID, entry.KeyUsage{Up: 100, Down: 1000}},
		{"r1", boot1, k.vlessID, entry.KeyUsage{Up: 50, Down: 500}}, // late pulse
		{"r1", boot2, k.vlessID, entry.KeyUsage{Up: 10, Down: 20}},  // edge restarted
		{"r2", boot1, k.hy2Auth, entry.KeyUsage{Up: 1, Down: 2}},    // another relay
		{"r2", boot1, uuid.NewString(), entry.KeyUsage{Up: 9, Down: 9}},
	} {
		if err := s.RecordUsage(ctx, step.relay, step.boot, map[string]entry.KeyUsage{step.key: step.u}); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.Usage(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if u != (entry.KeyUsage{Up: 111, Down: 1022}) {
		t.Fatalf("usage %+v", u)
	}
}

// The links a subscriber imports carry their own key, never the relay's
// shared credential.
func TestLinksCarryTheSubscribersKey(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	batch := &fakeBatch{envelopes: [][]byte{signed(t, signer, realityRecord()), signed(t, signer, xhttpRecord())}}
	s := New(pool, batch, []entry.Key{signer.Key()})
	ctx := context.Background()
	name := "t-" + uuid.NewString()[:8]
	token, err := s.Add(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	k := keyOf(t, pool, name)
	links, err := s.Links(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("links %q", links)
	}
	for _, l := range links {
		if !strings.HasPrefix(l, "vless://"+k.vlessID+"@") {
			t.Fatalf("link without the subscriber's key: %s", l)
		}
	}
}
