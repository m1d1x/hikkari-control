package main

import (
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/proxy"
)

const (
	keyA = "00000000-0000-4000-8000-00000000000a"
	keyB = "00000000-0000-4000-8000-00000000000b"
)

// startXhttp runs an XHTTP edge's Xray on loopback, as run() would.
func startXhttp(t *testing.T) (*core.Instance, edge) {
	t.Helper()
	m, _ := xhttpEnv(t, freePort(t, "tcp"), freePort(t, "tcp"))
	prev := osEnv
	osEnv = env(m)
	t.Cleanup(func() { osEnv = prev })
	e, err := load()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := core.New(e.xray)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	return inst, e
}

func users(t *testing.T, inst *core.Instance, tag string) proxy.UserManager {
	t.Helper()
	im := inst.GetFeature(inbound.ManagerType()).(inbound.Manager)
	h, err := im.GetHandler(context.Background(), tag)
	if err != nil {
		t.Fatal(err)
	}
	return h.(proxy.GetInbound).GetInbound().(proxy.UserManager)
}

// The hub's list is the inbound's keys exactly: new keys come in, keys gone
// from the list go out. (The shared credential has no email, so Xray's user
// manager does not list it; the tunnel tests cover that it still dials.)
func TestTheKeyListBecomesTheInboundsUsers(t *testing.T) {
	inst, e := startXhttp(t)
	k, err := newKeyring(inst, e)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	um := users(t, inst, xhttpTag)

	if err := k.Set(ctx, []string{keyA, keyB}); err != nil {
		t.Fatal(err)
	}
	if um.GetUser(ctx, keyA) == nil || um.GetUser(ctx, keyB) == nil {
		t.Fatal("keys not added")
	}
	if err := k.Set(ctx, []string{keyB}); err != nil {
		t.Fatal(err)
	}
	if um.GetUser(ctx, keyA) != nil || um.GetUser(ctx, keyB) == nil {
		t.Fatal("the list was not applied exactly")
	}
	if err := k.Set(ctx, []string{}); err != nil {
		t.Fatal(err)
	}
	if n := um.GetUsersCount(ctx); n != 0 {
		t.Fatalf("keys left: %d", n)
	}
}

// Per-key totals are read off Xray's user counters, keyed by the key.
func TestKeyTotalsAreReadPerKey(t *testing.T) {
	inst, _ := startXhttp(t)
	sm := inst.GetFeature(stats.ManagerType()).(stats.Manager)
	for name, v := range map[string]int64{
		"user>>>" + keyA + ">>>traffic>>>uplink":   10,
		"user>>>" + keyA + ">>>traffic>>>downlink": 20,
	} {
		c, err := sm.RegisterCounter(name)
		if err != nil {
			t.Fatal(err)
		}
		c.Set(v)
	}
	// The inbound's own counter is there too and is not a key.
	if sm.GetCounter("inbound>>>xhttp>>>traffic>>>uplink") == nil {
		t.Fatal("inbound counter missing")
	}
	got := keyTotals(sm)
	if len(got) != 1 || got[keyA] != (pulseKey{Up: 10, Down: 20}) {
		t.Fatalf("totals %+v", got)
	}
}

// A subscriber's key dials through a real edge and its bytes are counted
// under the key; once the hub's list drops the key, the same client is
// turned away while the shared credential still goes through.
func TestAKeyDialsIsCountedAndIsCutOff(t *testing.T) {
	edgePort, local, sitePort := freePort(t, "tcp"), freePort(t, "tcp"), freePort(t, "tcp")
	tcpEcho(t, sitePort, "site")
	m, pin := xhttpEnv(t, edgePort, local)
	_, inst := startXhttpEdge(t, m, sitePort)
	k, err := newKeyring(inst, edge{proto: protoXhttp, userTags: []string{xhttpTag}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := k.Set(ctx, []string{keyA}); err != nil {
		t.Fatal(err)
	}

	door := xhttpClientAs(t, keyA, edgePort, pin, "stream-one", map[string]int{"site": sitePort})["site"]
	if got := talk(t, door, 5*time.Second); got != "site:ping" {
		t.Fatalf("through the tunnel on a key: %q", got)
	}
	sm := inst.GetFeature(stats.ManagerType()).(stats.Manager)
	if u := keyTotals(sm)[keyA]; u.Up == 0 || u.Down == 0 {
		t.Fatalf("the key's bytes were not counted: %+v", u)
	}

	if err := k.Set(ctx, []string{}); err != nil {
		t.Fatal(err)
	}
	door = xhttpClientAs(t, keyA, edgePort, pin, "stream-one", map[string]int{"site": sitePort})["site"]
	if got := talk(t, door, 2*time.Second); got != "" {
		t.Fatalf("a dropped key still dials: %q", got)
	}
	shared := xhttpClient(t, edgePort, pin, "stream-one", map[string]int{"site": sitePort})["site"]
	if got := talk(t, shared, 5*time.Second); got != "site:ping" {
		t.Fatalf("the shared credential: %q", got)
	}
}
