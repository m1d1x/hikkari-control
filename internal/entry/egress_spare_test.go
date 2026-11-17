package entry

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// The receiver's env names both egresses by box and key; without a spare it
// has no SPARE_ line at all, so the watch has nothing to move to.
func TestTheEnvNamesTheSpareBesideTheEgress(t *testing.T) {
	eg := netip.MustParseAddr("203.0.113.20")
	sp := &egressSpare{box: "aez8", pub: "spPubKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", addr: netip.MustParseAddr("203.0.113.30")}
	env := receiverEnv("aez9", "egPubKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", eg, 1, sp)
	for _, want := range []string{"PEER_BOX=aez9\n", "SPARE_BOX=aez8\n", "SPARE_PUB=" + sp.pub + "\n", "SPARE_ENDPOINT=203.0.113.30:51821\n"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env has no %q:\n%s", want, env)
		}
	}
	if env := receiverEnv("aez9", "egPubKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", eg, 1, nil); strings.Contains(env, "SPARE_") {
		t.Fatalf("an env without a spare names one:\n%s", env)
	}
}

// The spare is a peer of the link with only its own address: the default
// routes stay one peer's, the spare is the watch's probe; the hub's apply
// gives the routes to the hub's egress unless told to keep the spare.
func TestTheSpareIsAPeerWithItsOwnAddressOnly(t *testing.T) {
	_, peer, _ := strings.Cut(egressUpScript, "receiver_peer() {")
	peer, _, _ = strings.Cut(peer, "\n}\n")
	for _, want := range []string{
		`[ "${1:-}" = keep ] && [ -n "${SPARE_PUB:-}" ] && [ "$(active_pub)" = "$SPARE_PUB" ]`,
		`allowed-ips "$(host_of "$other")/32"`,
		`[ "$p" = "$PEER_PUB" ] || [ "$p" = "${SPARE_PUB:-}" ] || wg set "$ifc" peer "$p" remove`,
	} {
		if !strings.Contains(peer, want) {
			t.Fatalf("receiver_peer has no %q", want)
		}
	}
	if !strings.Contains(egressApplyScript, egressScript+" apply ${1:-}") || !strings.Contains(egressApplyScript, "systemctl restart "+egressWatchUnit) {
		t.Fatal("the apply does not pass keep or does not restart the watch")
	}
}

// The watch moves in one wg call that gives the spare the routes and leaves
// the dead egress its own address, so it is still watched and can carry
// again; it never moves to a spare it has not heard, nor twice within the
// hold.
func TestTheWatchMovesInOneCallToASpareItHears(t *testing.T) {
	_, w, _ := strings.Cut(egressUpScript, "watch_egresses() {")
	w, _, _ = strings.Cut(w, "\n}\n")
	move := `wg set "$ifc" peer "$sb" allowed-ips 0.0.0.0/0,::/0 peer "$act" allowed-ips "$(host_of "$act")/32"`
	if !strings.Contains(w, move) {
		t.Fatal("the watch does not move in one wg call")
	}
	guard := `if [ "$heard" = 1 ] && [ $((now - moved_at)) -ge "$hold" ]; then`
	if !strings.Contains(w, guard) || strings.Index(w, guard) > strings.Index(w, move) {
		t.Fatal("the watch moves without hearing the spare or within the hold")
	}
	// The tunnel is judged faster than the internet behind the egress: a
	// move breaks every connection, and public resolvers blink on their own.
	if !strings.Contains(w, "red_tun=${RED_TUN_MS:-1500}") || !strings.Contains(w, "red_net=${RED_NET_MS:-5000}") {
		t.Fatal("the watch's thresholds are not 1.5 s for the tunnel and 5 s for the internet")
	}
	if !strings.Contains(egressUpScript, "ping -D -n -i 0.2 -I") {
		t.Fatal("the tunnel is not pinged every 200 ms")
	}
	// The why is found after the move: the ping past the tunnel costs up
	// to a second.
	if strings.Index(w, move) > strings.Index(w, `ping_rtt "$(host_of "$dead")"`) {
		t.Fatal("the watch pings past the tunnel before it moves")
	}
	for _, line := range strings.Split(w, "\n") {
		if strings.Contains(line, `echo "egress:`) && (strings.Contains(line, "host_of") || strings.Contains(line, "ENDPOINT")) {
			t.Fatalf("a line of the watch says an address: %s", line)
		}
	}
}

// Making the spare: the spare carries the receiver beside its other
// receivers, the receiver's env names it, the apply keeps the egress that
// carries, the receiver hears the spare through the tunnel.
func TestASpareStandsBesideTheEgressThatCarries(t *testing.T) {
	p := newEgressPair()
	p.ends.egBox = "aez8"
	p.ends.egAddr = netip.MustParseAddr("203.0.113.30")
	carrier := &carrying{box: "aez9", pub: "cuPubKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", addr: netip.MustParseAddr("203.0.113.20")}
	other := egressPeer{box: "unl8", pub: "unl8PubAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", slot: 3, addr: netip.MustParseAddr("198.51.100.8")}
	sp, err := standEgressSpare(context.Background(), p.ends, carrier, 1, []egressPeer{other}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sp.box != "aez8" || sp.pub != p.eg.pub {
		t.Fatalf("spare %+v", sp)
	}
	if peers := p.eg.files[egressDir+"/peers"]; peers != other.pub+" 10.231.0.3 fd77:231::3 198.51.100.8\n"+p.rx.pub+" 10.231.0.1 fd77:231::1 198.51.100.10\n" {
		t.Fatalf("spare's peers:\n%s", peers)
	}
	env := p.rx.files[egressDir+"/env"]
	if !strings.Contains(env, "PEER_PUB="+carrier.pub) || !strings.Contains(env, "SPARE_PUB="+p.eg.pub) {
		t.Fatalf("receiver env:\n%s", env)
	}
	var keep, heard bool
	for _, c := range p.rx.cmds {
		keep = keep || c == "sh -s -- keep"
		heard = heard || strings.Contains(c, "ping -n -q -c3 -W1 -I hk-egress 203.0.113.30")
	}
	if !keep || !heard {
		t.Fatalf("apply kept the carrier %v, the spare heard %v", keep, heard)
	}
	if p.rx.files["/etc/systemd/system/"+egressWatchUnit+".service"] != egressWatchUnitFile {
		t.Fatal("the receiver has no watch")
	}
	if _, ok := p.eg.files["/etc/systemd/system/"+egressWatchUnit+".service"]; ok {
		t.Fatal("an egress got the receiver's watch")
	}
}

// The hub keeps a receiver's spare beside its egress: the spare carries the
// receiver too, may be neither the egress nor the receiver, and goes away.
func TestTheHubKeepsTheSpare(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	s := &Service{pool: pool}
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `DELETE FROM entry_egress_links`); err != nil {
		t.Fatal(err)
	}
	for box, addr := range map[string]string{"sprx1": "198.51.100.11", "speg1": "203.0.113.11", "speg2": "203.0.113.12"} {
		ct, err := cryptobox.Encrypt([]byte(addr))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, $2, 'test', 'test', 'hikkari') ON CONFLICT (name) DO UPDATE SET address_ct = $2`, box, ct); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.saveEgressLink(ctx, "sprx1", "speg1", 1, "rx1pub"); err != nil {
		t.Fatal(err)
	}
	if sp, err := s.egressSpareOf(ctx, "sprx1"); err != nil || sp != nil {
		t.Fatalf("a spare before one was made: %+v %v", sp, err)
	}
	if err := s.saveEgressSpare(ctx, "sprx1", "speg2", "sp2pub"); err != nil {
		t.Fatal(err)
	}
	sp, err := s.egressSpareOf(ctx, "sprx1")
	if err != nil || sp == nil || sp.box != "speg2" || sp.pub != "sp2pub" || sp.addr.String() != "203.0.113.12" {
		t.Fatalf("spare %+v %v", sp, err)
	}
	if peers, err := s.egressPeersOf(ctx, "speg2", ""); err != nil || len(peers) != 1 || peers[0].box != "sprx1" || peers[0].slot != 1 {
		t.Fatalf("the spare does not carry the receiver: %+v %v", peers, err)
	}
	rxs, err := s.EgressReceivers(ctx)
	if err != nil || len(rxs) != 1 || rxs[0] != (EgressReceiver{Box: "sprx1", Egress: "speg1", Spare: "speg2"}) {
		t.Fatalf("receivers %+v %v", rxs, err)
	}
	if err := s.saveEgressSpare(ctx, "sprx1", "speg1", "x"); err == nil {
		t.Fatal("the egress is its own spare")
	}
	if err := s.saveEgressSpare(ctx, "sprx1", "", ""); err != nil {
		t.Fatal(err)
	}
	if peers, err := s.egressPeersOf(ctx, "speg2", ""); err != nil || len(peers) != 0 {
		t.Fatalf("the old spare still carries: %+v %v", peers, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM entry_egress_links`); err != nil {
		t.Fatal(err)
	}
}
