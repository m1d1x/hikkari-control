package entry

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

func TestRIPEsRussianBlocksBecomePrefixes(t *testing.T) {
	in := `2|ripencc|1763337599|1|19830705|20261116|+0100
ripencc|*|ipv4|*|1|summary
ripencc|RU|ipv4|77.88.0.0|16384|20060608|allocated|x
ripencc|RU|ipv4|5.8.0.0|768|20120101|assigned|x
ripencc|NL|ipv4|193.39.143.0|256|20200101|allocated|x
ripencc|RU|ipv4|1.2.3.0|256|20200101|reserved|x
ripencc|RU|ipv6|2a02:6b8::|32|20070101|allocated|x
ripencc|RU|asn|13238|1|20070101|allocated|x
`
	v4, v6, err := parseDelegated(strings.NewReader(in), "RU")
	if err != nil {
		t.Fatal(err)
	}
	want4 := []string{"77.88.0.0/18", "5.8.0.0/23", "5.8.2.0/24"}
	if len(v4) != len(want4) {
		t.Fatalf("v4 %v", v4)
	}
	for i, p := range v4 {
		if p.String() != want4[i] {
			t.Fatalf("v4 %v, want %v", v4, want4)
		}
	}
	if len(v6) != 1 || v6[0].String() != "2a02:6b8::/32" {
		t.Fatalf("v6 %v", v6)
	}
	el := ruNetsElements(v4, v6)
	for _, want := range []string{"add element inet hikkari_egress ru4 { 77.88.0.0/18, 5.8.0.0/23, 5.8.2.0/24 }", "add element inet hikkari_egress ru6 { 2a02:6b8::/32 }"} {
		if !strings.Contains(el, want) {
			t.Fatalf("no %q in\n%s", want, el)
		}
	}
}

func TestARangeIsCoveredExactly(t *testing.T) {
	for _, c := range []struct {
		start string
		n     uint32
	}{{"10.0.0.0", 1}, {"10.0.0.1", 255}, {"10.0.1.128", 1000}, {"0.0.0.0", 3}, {"255.255.255.0", 256}} {
		ps := rangeToPrefixes(netip.MustParseAddr(c.start), c.n)
		var total uint64
		next := netip.MustParseAddr(c.start)
		for _, p := range ps {
			if p.Addr() != next || p != p.Masked() {
				t.Fatalf("%s+%d: %v not contiguous and aligned", c.start, c.n, ps)
			}
			size := uint64(1) << (32 - p.Bits())
			total += size
			for range size {
				next = next.Next()
			}
		}
		if total != uint64(c.n) {
			t.Fatalf("%s+%d: %v covers %d", c.start, c.n, ps, total)
		}
	}
}

// egressBox answers like a box of the tunnel and keeps what was written.
type egressBox struct {
	pub        string
	files      map[string]string
	cmds       []string
	ruCut      int
	tunnelDown bool
	sees       string // the address the trace answers with
	ruAnswers  bool
}

func (b *egressBox) Run(_ context.Context, cmd string, stdin io.Reader) (string, error) {
	b.cmds = append(b.cmds, cmd)
	if stdin != nil {
		body, _ := io.ReadAll(stdin)
		if _, path, ok := strings.Cut(cmd, "cat > "); ok {
			b.files[path] = string(body)
		}
	}
	switch {
	case strings.HasPrefix(cmd, "bash -s -- "):
		return "Reading package lists\n" + b.pub + "\n", nil
	case strings.HasPrefix(cmd, "nft list counter"):
		return "counter ru_cut {\n\tpackets " + strconv.Itoa(b.ruCut) + " bytes 0\n}\n", nil
	case cmd == "systemctl stop "+egressUnit:
		b.tunnelDown = true
	case cmd == "systemctl start "+egressUnit:
		b.tunnelDown = false
	case strings.Contains(cmd, egressCheckRU):
		if b.ruAnswers {
			return "", nil
		}
		return "", errors.New("exit status 7")
	case strings.Contains(cmd, "curl -4"):
		if b.tunnelDown {
			return "", errors.New("exit status 28")
		}
		return "fl=1\nip=" + b.sees + "\nts=1\n", nil
	case strings.Contains(cmd, "curl -6"):
		return "", errors.New("exit status 7")
	}
	return "", nil
}
func (b *egressBox) HostKey() string { return "" }
func (b *egressBox) Close() error    { return nil }

// egressPair is a receiver and an egress whose probe the egress's counter
// follows.
type egressPair struct {
	rx, eg *egressBox
	ends   *egressEnds
}

func newEgressPair() *egressPair {
	nop := func(string, ...any) {}
	rx := &egressBox{pub: "rxPubKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", files: map[string]string{}, sees: "203.0.113.20"}
	eg := &egressBox{pub: "egPubKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", files: map[string]string{}}
	return &egressPair{rx: rx, eg: eg, ends: &egressEnds{
		rx: rx, eg: &ruCounting{egressBox: eg, rx: rx},
		rxAddr: netip.MustParseAddr("198.51.100.10"), egAddr: netip.MustParseAddr("203.0.113.20"),
		rxLog: nop, egLog: nop, rxBox: "unl9", egBox: "aez9",
		ruNets: "add element inet hikkari_egress ru4 { 77.88.0.0/18 }\n",
	}}
}

// ruCounting bumps the egress's cut counter whenever the receiver's probe
// to a Russian address was refused since the last read.
type ruCounting struct {
	*egressBox
	rx   *egressBox
	seen int
}

func (r *ruCounting) Run(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	n := 0
	for _, c := range r.rx.cmds {
		if strings.Contains(c, egressCheckRU) {
			n++
		}
	}
	if !r.rx.ruAnswers {
		r.egressBox.ruCut = n
	}
	return r.egressBox.Run(ctx, cmd, stdin)
}

// TestTheTunnelStandsOnBothEndsAndChecksItself: each end's key stays on its
// box and the other end gets only its public key; the egress gets the
// Russian blocks; the check passes on an egress the internet sees.
func TestTheTunnelStandsOnBothEndsAndChecksItself(t *testing.T) {
	p := newEgressPair()
	ctx := context.Background()
	rxPub, err := standEgress(ctx, p.ends, 1, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if rxPub != p.rx.pub {
		t.Fatalf("receiver's key %q", rxPub)
	}
	rxEnv, egEnv := p.rx.files[egressDir+"/env"], p.eg.files[egressDir+"/env"]
	for _, want := range []string{"ROLE=receiver", "ADDR4=10.231.0.1\n", "ADDR6=fd77:231::1\n", "PEER_PUB=" + p.eg.pub, "PEER_ENDPOINT=203.0.113.20:51821"} {
		if !strings.Contains(rxEnv, want) {
			t.Fatalf("receiver env has no %q:\n%s", want, rxEnv)
		}
	}
	if !strings.Contains(egEnv, "ROLE=egress") {
		t.Fatalf("egress env:\n%s", egEnv)
	}
	if peers := p.eg.files[egressDir+"/peers"]; peers != p.rx.pub+" 10.231.0.1 fd77:231::1 198.51.100.10\n" {
		t.Fatalf("egress peers:\n%s", peers)
	}
	if !strings.Contains(p.eg.files[egressDir+"/ru.nft"], "77.88.0.0/18") || p.rx.files[egressDir+"/ru.nft"] != "" {
		t.Fatalf("Russian blocks: egress %q, receiver %q", p.eg.files[egressDir+"/ru.nft"], p.rx.files[egressDir+"/ru.nft"])
	}
	for _, b := range []*egressBox{p.rx, p.eg} {
		for path, body := range b.files {
			if strings.Contains(body, "private-key") && path != egressScript {
				t.Fatalf("%s carries a key", path)
			}
		}
		if b.files[egressScript] != egressUpScript || b.files["/etc/systemd/system/"+egressUnit+".service"] != egressUnitFile {
			t.Fatal("the tunnel's program or unit is not on a box")
		}
	}
	if err := checkEgress(ctx, p.ends); err != nil {
		t.Fatal(err)
	}
	if p.rx.tunnelDown {
		t.Fatal("the check left the tunnel down")
	}
}

func TestTheCheckFailsWhenTheReceiverShowsThroughOrRussiaAnswers(t *testing.T) {
	ctx := context.Background()
	p := newEgressPair()
	p.rx.sees = "198.51.100.10"
	if err := checkEgress(ctx, p.ends); err == nil || !strings.Contains(err.Error(), "не адрес выхода") {
		t.Fatalf("the receiver's own address passed: %v", err)
	}
	p = newEgressPair()
	p.rx.ruAnswers = true
	if err := checkEgress(ctx, p.ends); err == nil || !strings.Contains(err.Error(), "сети РФ") {
		t.Fatalf("a Russian answer passed: %v", err)
	}
}

// TestTheReceiversGuardIsTheKernels: the routed user's connections go into
// the tunnel by mark, anything of theirs on another interface is dropped,
// and with the link gone an unreachable route stays; answers to connections
// made to the box are not marked.
func TestTheReceiversGuardIsTheKernels(t *testing.T) {
	s := egressUpScript
	for _, want := range []string{
		"meta skuid $uid ct direction original fib daddr type != local meta mark set 0x4377",
		`meta skuid $uid ct direction original oifname != { "lo", "$ifc" } counter name "guard_drop" drop`,
		"ip $fam route replace unreachable default metric 4096 table 4377",
		"ip $fam rule add fwmark 0x4377 lookup 4377 priority 4377",
		`iifname "$ifc" ip daddr @ru4 counter name "ru_cut" reject`,
		`iifname "$ifc" ip6 daddr @ru6 counter name "ru_cut" reject`,
		// The egress's own new connections too: the core's relay (G2).
		`ct state new ip daddr @ru4 counter name "ru_cut" reject`,
		`ct state new ip6 daddr @ru6 counter name "ru_cut" reject`,
		"port=" + strconv.Itoa(EgressPort),
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("the tunnel's program has no %q", want)
		}
	}
	// down takes only the link: the guard and the unreachable route stay.
	_, down, _ := strings.Cut(s, "\ndown)\n")
	down, _, _ = strings.Cut(down, ";;")
	if strings.Contains(down, "nft") || strings.Contains(down, "rule del") {
		t.Fatalf("down takes the guard away:\n%s", down)
	}
	// The guard is loaded before the link exists.
	_, rx, _ := strings.Cut(s, "receiver_up() {")
	if strings.Index(rx, "receiver_guard") > strings.Index(rx, "ip link add") {
		t.Fatal("the receiver's link comes up before its guard")
	}
}

func TestASlotIsTheReceiversTunnelAddress(t *testing.T) {
	for slot, want := range map[int][2]string{1: {"10.231.0.1", "fd77:231::1"}, 3: {"10.231.0.3", "fd77:231::3"}, 258: {"10.231.1.2", "fd77:231::102"}} {
		if v4, v6 := egressSlotAddrs(slot); v4 != want[0] || v6 != want[1] {
			t.Fatalf("slot %d: %s %s", slot, v4, v6)
		}
	}
	if v4, v6 := egressSlotAddrs(2); v4 != egressTunnelV4 || v6 != egressTunnelV6 {
		t.Fatal("slot 2 is not the egress's own end, which no receiver may take")
	}
}

// An egress carries every receiver of its peers: a second receiver stands
// beside the first, and the first keeps its line.
func TestAnEgressCarriesEveryReceiverBesideTheOthers(t *testing.T) {
	p := newEgressPair()
	first := egressPeer{box: "unl8", pub: "unl8PubAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", slot: 1, addr: netip.MustParseAddr("198.51.100.8")}
	if _, err := standEgress(context.Background(), p.ends, 3, []egressPeer{first}, false); err != nil {
		t.Fatal(err)
	}
	want := first.pub + " 10.231.0.1 fd77:231::1 198.51.100.8\n" + p.rx.pub + " 10.231.0.3 fd77:231::3 198.51.100.10\n"
	if peers := p.eg.files[egressDir+"/peers"]; peers != want {
		t.Fatalf("egress peers:\n%s", peers)
	}
	if !strings.Contains(p.rx.files[egressDir+"/env"], "ADDR4=10.231.0.3\n") {
		t.Fatal("the second receiver took another's tunnel address")
	}
}

// A move the internet does not see through points the receiver back at the
// egress it went through and lets it go from the new one; the probe call runs
// on the receiver as the routed user, around the swap.
func TestAMoveThatDoesNotCarryGoesBack(t *testing.T) {
	lead, lookup := egressCallLead, egressCallLookup
	egressCallLead = 0
	egressCallLookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("162.159.207.0")}, nil
	}
	defer func() { egressCallLead, egressCallLookup = lead, lookup }()

	p := newEgressPair()
	p.rx.sees = "198.51.100.10"
	var backTo int
	p.ends.back = func(_ context.Context, slot int) error {
		backTo = slot
		return nil
	}
	other := egressPeer{box: "unl8", pub: "unl8PubAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", slot: 1, addr: netip.MustParseAddr("198.51.100.8")}
	if _, err := standEgress(context.Background(), p.ends, 4, []egressPeer{other}, true); err == nil || !strings.Contains(err.Error(), "не адрес выхода") {
		t.Fatalf("a move through a dead egress passed: %v", err)
	}
	if backTo != 4 {
		t.Fatal("the receiver did not go back to its egress")
	}
	if peers := p.eg.files[egressDir+"/peers"]; peers != other.pub+" 10.231.0.1 fd77:231::1 198.51.100.8\n" {
		t.Fatalf("the new egress kept the receiver:\n%s", peers)
	}
	call := ""
	for _, c := range p.rx.cmds {
		if strings.Contains(c, egressCallUnit) && strings.Contains(c, "systemd-run") {
			call = c
		}
	}
	if !strings.Contains(call, "--uid="+EgressUser) || !strings.Contains(call, "162.159.207.0") {
		t.Fatalf("the probe call is not the routed user's: %q", call)
	}
}

// apply on a live link never takes it down: the receiver's new peer takes the
// default routes before the old one goes, and its address stays.
func TestApplyMovesTheLinkInPlace(t *testing.T) {
	_, apply, _ := strings.Cut(egressUpScript, "\napply)\n")
	apply, _, _ = strings.Cut(apply, ";;")
	_, live, _ := strings.Cut(apply, "elif")
	if strings.Contains(live, "link del") || strings.Contains(live, "_up") {
		t.Fatalf("apply rebuilds a live link:\n%s", live)
	}
	// A live egress gets its table again in place: the Russian cut of a
	// newer hub (box_out, G2) without a new link.
	if _, egressLive, _ := strings.Cut(live, "else"); !strings.Contains(egressLive, "egress_table") {
		t.Fatalf("an apply to a live egress leaves its table as it was:\n%s", live)
	}
	_, peer, _ := strings.Cut(egressUpScript, "receiver_peer() {")
	peer, _, _ = strings.Cut(peer, "\n}\n")
	if strings.Index(peer, `allowed-ips 0.0.0.0/0,::/0`) > strings.Index(peer, "remove") {
		t.Fatal("the old egress goes before the new one holds the routes")
	}
	if !strings.Contains(egressApplyScript, egressScript+" apply") {
		t.Fatal("the hub does not apply a live link in place")
	}
}

func TestATunnelNeedsAnEgressOfThatRole(t *testing.T) {
	if !wgKeyRe.MatchString("egPubKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") || wgKeyRe.MatchString("not a key") {
		t.Fatal("wg key pattern")
	}
	p := &Provisioner{}
	if _, err := p.openEgress(context.Background(), "unl9", "unl9"); !errors.Is(err, ErrRejected) {
		t.Fatalf("one box as both ends: %v", err)
	}
}

// TestTheKeyIsWhereWgMayReadIt: Ubuntu's AppArmor profile of wg reads keys
// only under /etc/wireguard; a key elsewhere fails the tunnel at "fopen:
// Permission denied" (aeznl1, 16.11).
func TestTheKeyIsWhereWgMayReadIt(t *testing.T) {
	if !strings.HasPrefix(egressKey, "/etc/wireguard/") {
		t.Fatalf("key at %s", egressKey)
	}
	for _, s := range []string{egressUpScript, egressPrepareScript} {
		if strings.Contains(s, "< "+egressDir+"/private") || strings.Contains(s, "> "+egressDir+"/private") || strings.Contains(s, `"$dir/private"`) {
			t.Fatal("a script still reads the key outside /etc/wireguard")
		}
	}
	if !strings.Contains(egressUpScript, "key="+egressKey) || !strings.Contains(egressPrepareScript, "wg genkey > "+egressKey) {
		t.Fatal("the scripts do not use the key in /etc/wireguard")
	}
}

// TestTheGuardLooksAfterTheReroute: in the output hook the packet still
// carries the device it had before the mark rerouted it, so a guard there
// drops what goes into the tunnel (unl1, 16.11: guard_drop 7, the probe's
// SYNs). The guard sits in postrouting.
func TestTheGuardLooksAfterTheReroute(t *testing.T) {
	_, guard, ok := strings.Cut(egressUpScript, "chain guard_post {")
	if !ok {
		t.Fatal("no guard chain")
	}
	guard, _, _ = strings.Cut(guard, "}\n")
	if !strings.Contains(guard, "hook postrouting") || !strings.Contains(guard, `counter name "guard_drop" drop`) {
		t.Fatalf("guard:\n%s", guard)
	}
}

// TestTheTunnelsOwnPacketsAreNotMarkedAgain: WireGuard's packet keeps the
// socket of the packet it wraps, so the routed user's uid is on it too; its
// own fwmark keeps it out of the mark and past the guard (unl1, 16.11: a
// loop, each lap 64 bytes longer).
func TestTheTunnelsOwnPacketsAreNotMarkedAgain(t *testing.T) {
	s := egressUpScript
	for _, want := range []string{
		`listen-port "$port" fwmark 0x4378`,
		"meta mark != 0x4378 meta skuid $uid ct direction original fib daddr type != local meta mark set 0x4377",
		`meta mark != 0x4378 meta skuid $uid ct direction original oifname != { "lo", "$ifc" } counter name "guard_drop" drop`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %q", want)
		}
	}
}

// relayBox answers the relay move like a receiver whose relay came up
// under the uid it is given.
type relayBox struct {
	*egressBox
	uid string
}

func (r *relayBox) Run(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	if strings.HasPrefix(cmd, "sh -s -- ") {
		r.egressBox.cmds = append(r.egressBox.cmds, cmd)
		in := "no"
		if r.uid == "999" {
			in = "yes"
		}
		return "unl9-2 in=" + in + " uids=" + r.uid + " \n", nil
	}
	return r.egressBox.Run(ctx, cmd, stdin)
}

// TestARelayGoesOutOnlyThroughTheTunnel: a relay moved in runs as the routed
// user, resolves through the tunnel, waits for the tunnel and does not start
// without the guard; a relay that came up under another uid fails the move.
func TestARelayGoesOutOnlyThroughTheTunnel(t *testing.T) {
	ctx := context.Background()
	for _, want := range []string{"User=" + EgressUser, "Group=" + EgressUser, "After=" + egressUnit + ".service", "ExecStartPre=+/usr/sbin/nft list chain inet hikkari_egress guard_post", "BindReadOnlyPaths=" + egressDir + "/resolv.conf:/etc/resolv.conf"} {
		if !strings.Contains(egressRelayDropIn, want) {
			t.Fatalf("drop-in has no %q", want)
		}
	}
	if strings.Contains(egressRelayDropIn, "Requires=") || strings.Contains(egressRelayDropIn, "BindsTo=") {
		t.Fatal("a relay must not stop with the tunnel's unit: the guard holds it, a stop would cut people")
	}
	if !strings.Contains(egressRelaysScript, egressRelayDropIn) {
		t.Fatal("the script does not write the drop-in")
	}
	p := newEgressPair()
	rx := &relayBox{egressBox: p.rx, uid: "999"}
	p.ends.rx = rx
	if err := egressRelays(ctx, p.ends, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.rx.files[egressDir+"/resolv.conf"], "nameserver 1.1.1.1") {
		t.Fatalf("resolver: %q", p.rx.files[egressDir+"/resolv.conf"])
	}
	rx.uid = "61234"
	if err := egressRelays(ctx, p.ends, true); err == nil {
		t.Fatal("a relay under another uid passed the move")
	}
	p.rx.sees = "198.51.100.10"
	rx.uid = "999"
	n := len(p.rx.cmds)
	if err := egressRelays(ctx, p.ends, true); err == nil {
		t.Fatal("relays moved onto a tunnel the internet sees through")
	}
	for _, c := range p.rx.cmds[n:] {
		if strings.HasPrefix(c, "sh -s -- ") {
			t.Fatal("relays restarted before the tunnel was checked")
		}
	}
}

// The hub keeps who goes through whom (migration 072): the first receiver gets
// slot 1, the address the single tunnel of H1 had; slot 2 is the egress's own
// end; a move keeps the receiver's slot; an egress's peers are the receivers
// it carries, each with its own address.
func TestTheHubKeepsWhoGoesOutThroughWhom(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	s := &Service{pool: pool}
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `DELETE FROM entry_egress_links`); err != nil {
		t.Fatal(err)
	}
	for box, addr := range map[string]string{"egrx1": "198.51.100.1", "egrx2": "198.51.100.2", "egeg1": "203.0.113.1", "egeg2": "203.0.113.2"} {
		ct, err := cryptobox.Encrypt([]byte(addr))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, $2, 'test', 'test', 'hikkari') ON CONFLICT (name) DO UPDATE SET address_ct = $2`, box, ct); err != nil {
			t.Fatal(err)
		}
	}
	free := func() int {
		n, err := s.freeEgressSlot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := free(); n != 1 {
		t.Fatalf("first slot %d", n)
	}
	if err := s.saveEgressLink(ctx, "egrx1", "egeg1", 1, "rx1pub"); err != nil {
		t.Fatal(err)
	}
	if n := free(); n != 3 {
		t.Fatalf("second slot %d, want 3: 2 is the egress's end", n)
	}
	if err := s.saveEgressLink(ctx, "egrx2", "egeg1", 3, "rx2pub"); err != nil {
		t.Fatal(err)
	}
	peers, err := s.egressPeersOf(ctx, "egeg1", "egrx2")
	if err != nil || len(peers) != 1 || peers[0].box != "egrx1" || peers[0].pub != "rx1pub" || peers[0].slot != 1 || peers[0].addr.String() != "198.51.100.1" {
		t.Fatalf("egeg1 without egrx2: %+v %v", peers, err)
	}
	// A move keeps the slot.
	if err := s.saveEgressLink(ctx, "egrx1", "egeg2", 1, "rx1pub"); err != nil {
		t.Fatal(err)
	}
	if eg, slot, ok, err := s.egressLink(ctx, "egrx1"); err != nil || !ok || eg != "egeg2" || slot != 1 {
		t.Fatalf("egrx1 after the move: %s %d %v %v", eg, slot, ok, err)
	}
	if peers, err := s.egressPeersOf(ctx, "egeg1", ""); err != nil || len(peers) != 1 || peers[0].box != "egrx2" {
		t.Fatalf("egeg1 after egrx1 left: %+v %v", peers, err)
	}
	if err := s.saveEgressLink(ctx, "egrx2", "egeg2", 1, "rx2pub"); err == nil {
		t.Fatal("two receivers hold one slot")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO entry_egress_links (receiver, egress, slot, rx_pub) VALUES ('egeg1', 'egeg2', 2, 'x')`); err == nil {
		t.Fatal("a receiver took the egress's end")
	}
	if err := s.deleteEgressLink(ctx, "egrx1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.egressLink(ctx, "egrx1"); err != nil || ok {
		t.Fatalf("egrx1 still linked: %v %v", ok, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM entry_egress_links`); err != nil {
		t.Fatal(err)
	}
}
