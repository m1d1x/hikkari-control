package entry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A box with a role (migration 070) is outside the relay pool: provisioning
// lays the hub's key and closes the box, and no relay is ever minted there.
// An egress's address goes to the internet, a clean receiver's must never
// reach a subscription (hikkari-client
// plans/2026-11-16-hidden-border-receiver-and-egress.md): a relay minted on
// either would hand its address to every tester at once.
func TestABoxWithARoleGetsTheHubsKeyAndNoRelay(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	for _, role := range []string{RoleEgress, RoleReceiver} {
		u := uuid.New()
		box := boxName("r", u)
		id, err := ph.prov.CreateAndProvision(ctx, NewBox{
			BoxSpec:  BoxSpec{Name: box, Address: "2001:db8::" + u.String()[:4], Provider: "test", Location: "amsterdam", Role: role},
			Password: ph.box.password,
		}, "")
		if err != nil {
			t.Fatalf("%s: create and provision: %v", role, err)
		}
		j := ph.wait(id)
		log := ph.logText(id)
		if j.Status != "done" {
			t.Fatalf("%s: job %+v\n%s", role, j, log)
		}
		if !ph.box.keyIn || !ph.box.passwordOff {
			t.Fatalf("%s: hub key in: %v, password off: %v", role, ph.box.keyIn, ph.box.passwordOff)
		}
		relays, err := ph.svc.relayIdentities(ctx, box)
		if err != nil {
			t.Fatal(err)
		}
		if len(relays) != 0 {
			t.Fatalf("%s: relays %v on a box with a role\n%s", role, relayNames(relays), log)
		}
		var proto string
		var domain []byte
		if err := ph.pool.QueryRow(ctx, `SELECT proto, domain_ct FROM entry_boxes WHERE name = $1`, box).Scan(&proto, &domain); err != nil {
			t.Fatal(err)
		}
		if proto != ProtoHikkari || domain != nil {
			t.Fatalf("%s: proto %s, a domain claimed: %v\n%s", role, proto, domain != nil, log)
		}
		if strings.Contains(log, "nekko-edge") {
			t.Fatalf("%s: the edge went to a box with a role\n%s", role, log)
		}
		if _, err := ph.svc.AddRelay(ctx, box, KindPersonal); !errors.Is(err, ErrRejected) {
			t.Fatalf("%s: a relay minted on a box with a role: %v", role, err)
		}
		// The next box is a fresh machine to the fake.
		ph.box.keyIn, ph.box.passwordOff = false, false
	}
}

func TestABoxRoleIsEgressOrReceiverOnAHikkariBox(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	for _, b := range []BoxSpec{
		{Name: "ams901", Address: "203.0.113.9", Provider: "test", Location: "eu", Role: "exit"},
		{Name: "ams902", Address: "203.0.113.9", Provider: "test", Location: "eu", Role: RoleEgress, Proto: ProtoNaive},
		{Name: "ams903", Address: "203.0.113.9", Provider: "test", Location: "eu", Role: RoleReceiver, Proto: ProtoHikkari, Domain: "reef9001.lol"},
	} {
		if err := s.CreateBox(ctx, b); !errors.Is(err, ErrRejected) {
			t.Fatalf("%+v: %v", b, err)
		}
	}
}
