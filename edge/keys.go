package main

// keys.go lets subscribers through by access keys of their own, alongside the
// relay's shared credential (EDGE_CLIENTS / EDGE_AUTH).
//
// # Where the keys come from
//
// The hub answers every pulse with the full list of keys granted to this
// relay (pulse.go). The edge makes its inbounds' users exactly that list,
// adding and removing them in the running Xray — no restart, nobody else's
// connection dropped. The whole list each time: a relay that missed answers
// catches up on the next one, and a subscription the hub revoked is gone from
// every relay within one pulse.
//
// # What a key says about a person
//
// Nothing the relay could read back. A key is random, made by the hub and
// tied to an account only in the hub's database. The edge counts bytes per
// key, because a subscription's traffic limit is counted across every relay
// it has moved through, and the hub is the one place that can add them up.
// Holding the box gives away a list of random strings and their byte counts:
// no names, no payments, no addresses (the edge keeps none of those).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/proxy"
	hyaccount "github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/proxy/vless"
)

// userLevel is the Xray policy level access keys run at; withStats turns on
// per-user counters for it.
const userLevel = 0

// keyring is the set of access keys the running Xray lets through.
type keyring struct {
	inbounds inbound.Manager
	// tags are the inbounds a key is added to: every cover of a Reality edge,
	// the one tunnel inbound of the others.
	tags []string
	// account builds a key's Xray account in this relay's protocol.
	account func(key string) (protocol.Account, error)

	mu   sync.Mutex
	have map[string]bool
}

// newKeyring wires the keys of one edge into its running instance.
func newKeyring(inst *core.Instance, e edge) (*keyring, error) {
	im, _ := inst.GetFeature(inbound.ManagerType()).(inbound.Manager)
	if im == nil {
		return nil, fmt.Errorf("xray has no inbound manager")
	}
	k := &keyring{inbounds: im, tags: e.userTags, have: map[string]bool{}}
	switch e.proto {
	case protoReality:
		k.account = vlessAccount("xtls-rprx-vision")
	case protoXhttp:
		k.account = vlessAccount("")
	case protoHysteria:
		k.account = func(key string) (protocol.Account, error) {
			return (&hyaccount.Account{Auth: key}).AsAccount()
		}
	default:
		return nil, fmt.Errorf("keys: protocol %q", e.proto)
	}
	return k, nil
}

func vlessAccount(flow string) func(string) (protocol.Account, error) {
	return func(key string) (protocol.Account, error) {
		return (&vless.Account{Id: key, Flow: flow}).AsAccount()
	}
}

// Set makes the inbounds' access keys exactly keys. A key that fails is
// reported and left for the next answer; the others still apply.
func (k *keyring) Set(ctx context.Context, keys []string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	want := make(map[string]bool, len(keys))
	for _, key := range keys {
		want[key] = true
	}
	var errs []string
	for key := range k.have {
		if want[key] {
			continue
		}
		if err := k.each(ctx, func(um proxy.UserManager) error { return um.RemoveUser(ctx, key) }); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		delete(k.have, key)
	}
	for _, key := range sortedKeys(want) {
		if k.have[key] {
			continue
		}
		acc, err := k.account(key)
		if err != nil {
			errs = append(errs, fmt.Sprintf("key %.8s…: %v", key, err))
			continue
		}
		// The key is the user's email too: Xray counts per user by email,
		// and the hub files usage by key.
		user := &protocol.MemoryUser{Email: key, Level: userLevel, Account: acc}
		if err := k.each(ctx, func(um proxy.UserManager) error { return um.AddUser(ctx, user) }); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		k.have[key] = true
	}
	if len(errs) > 0 {
		return fmt.Errorf("keys: %s", strings.Join(errs, "; "))
	}
	return nil
}

// each runs f on every user inbound.
func (k *keyring) each(ctx context.Context, f func(proxy.UserManager) error) error {
	for _, tag := range k.tags {
		h, err := k.inbounds.GetHandler(ctx, tag)
		if err != nil {
			return fmt.Errorf("inbound %s: %w", tag, err)
		}
		gi, ok := h.(proxy.GetInbound)
		if !ok {
			return fmt.Errorf("inbound %s: no proxy", tag)
		}
		um, ok := gi.GetInbound().(proxy.UserManager)
		if !ok {
			return fmt.Errorf("inbound %s: users are fixed", tag)
		}
		if err := f(um); err != nil {
			return fmt.Errorf("inbound %s: %w", tag, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// keyTotals reads what every access key carried since the edge started.
// Xray names the counters user>>>email>>>traffic>>>{uplink,downlink}, and
// the email of an access key is the key itself; the shared credential has no
// email and is not counted here.
func keyTotals(counters stats.Manager) map[string]pulseKey {
	out := map[string]pulseKey{}
	counters.VisitCounters(func(name string, c stats.Counter) bool {
		rest, ok := strings.CutPrefix(name, "user>>>")
		if !ok {
			return true
		}
		key, dir, ok := strings.Cut(rest, ">>>traffic>>>")
		if !ok || key == "" {
			return true
		}
		t := out[key]
		switch dir {
		case "uplink":
			t.Up = c.Value()
		case "downlink":
			t.Down = c.Value()
		}
		out[key] = t
		return true
	})
	return out
}
