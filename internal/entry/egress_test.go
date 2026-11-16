package entry

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"testing"
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
	if err := standEgress(ctx, p.ends); err != nil {
		t.Fatal(err)
	}
	rxEnv, egEnv := p.rx.files[egressDir+"/env"], p.eg.files[egressDir+"/env"]
	for _, want := range []string{"ROLE=receiver", "PEER_PUB=" + p.eg.pub, "PEER_ENDPOINT=203.0.113.20:51821"} {
		if !strings.Contains(rxEnv, want) {
			t.Fatalf("receiver env has no %q:\n%s", want, rxEnv)
		}
	}
	for _, want := range []string{"ROLE=egress", "PEER_PUB=" + p.rx.pub, "PEER_ADDR=198.51.100.10"} {
		if !strings.Contains(egEnv, want) {
			t.Fatalf("egress env has no %q:\n%s", want, egEnv)
		}
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
	if strings.Index(rx, "nft -f -") > strings.Index(rx, "link_up") {
		t.Fatal("the receiver's link comes up before its guard")
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
