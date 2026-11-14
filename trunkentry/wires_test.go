package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/quicwire"
	"github.com/m1d1x/hikkari-protocol/trunk/udpwire"
)

// TestWiresFromTheFile: the entry starts with the file's wires, follows the
// file while it runs (a wire taken away closes, a wire given back comes
// up), and a client's UDP and TCP keep crossing, the UDP on a one-copy port
// as the voice class.
func TestWiresFromTheFile(t *testing.T) {
	const key = "trunk-key-0123456789abcdef-xyz"
	tcpEcho, udpEcho := echoes(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := trunk.NewExit(trunk.Config{}, loop{tcpEcho, udpEcho})
	go x.Run(ctx)
	serve := func(c io.ReadWriteCloser) { _ = x.ServeMember(c) }
	uport, qport := freePort(t), freePort(t)
	ul, err := udpwire.Listen("127.0.0.1:"+strconv.Itoa(uport), []string{key}, serve)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	ql, err := quicwire.Listen(ctx, quicwire.ListenOptions{Listen: "127.0.0.1:" + strconv.Itoa(qport), Keys: []string{key}, SNI: "hk.example", Serve: serve})
	if err != nil {
		t.Fatal(err)
	}
	defer ql.Close()

	path := filepath.Join(t.TempDir(), "wires.json")
	t.Setenv("EDGE_TRUNK_WIRES_FILE", path)
	wiresEvery = 50 * time.Millisecond
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path+".new", []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	both := `{"steer": true, "wires": [{"name": "udp", "kind": "udp", "port": ` + strconv.Itoa(uport) + `, "members": 2},
		{"name": "quic", "kind": "quic", "port": ` + strconv.Itoa(qport) + `, "members": 2}]}`
	write(both)

	dial := func(context.Context, int) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go func() { _ = x.ServeMember(b) }()
		return a, nil
	}
	port := freePort(t)
	c := Config{Exit: "127.0.0.1:443", SNI: "hk.example", Key: key, Members: 2,
		TCP: map[int]uint16{port: 443}, UDP: map[int]uint16{port: 443}, OneCopy: map[int]bool{port: true},
		PortLo: defaultPortLo, PortHi: defaultPortHi}
	entry, err := start(ctx, c, dial)
	if err != nil {
		t.Fatal(err)
	}
	go runWires(ctx, c, entry)
	up := func(udp, quic int) func() bool {
		return func() bool { w := entry.Wires(); return w[3].Members == udp && w[1].Members == quic }
	}
	waitUntil(t, up(2, 2))

	u, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	echoUDP(t, u, "voice")
	tc, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	echoTCP(t, tc, "bulk")

	write(`{"wires": [{"name": "quic", "kind": "quic", "port": ` + strconv.Itoa(qport) + `, "members": 2}]}`)
	waitUntil(t, up(0, 2))
	if r := entry.Session().Route(); r != trunk.AnyRoute {
		t.Fatalf("without steer the route is %+v", r)
	}
	write(both)
	waitUntil(t, up(2, 2))
	echoTCP(t, tc, "bulk after the change")

	write(`{not json`)
	time.Sleep(200 * time.Millisecond)
	if w := entry.Wires(); w[3].Members != 2 {
		t.Fatalf("a broken file changed what runs: %+v", w)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWireLines: a wire's lines are what the hub reads (internal/cascadewatch
// wire.go): the stand's keys, after the entry's prefix.
func TestWireLines(t *testing.T) {
	from := time.Unix(1_762_900_000, 0)
	call := wireCallLine("udp", trunk.CallWindow{Copies: 1, From: from, Sent: 3000, Got: 2999, RTTP50: 44 * time.Millisecond})
	if !regexp.MustCompile(`^call wire=udp copies=1 from=1762900000 sent=3000 got=2999 `).MatchString(call) {
		t.Fatalf("call line %q", call)
	}
	bulk := wireBulkLine("quic", from, trunk.DownloadResult{Want: 1 << 20, Got: 1 << 20, Took: 100 * time.Millisecond, End: "ok"})
	if bulk != "bulk wire=quic kind=1m from=1762900000 bytes=1048576 want=1048576 ms=100 end=ok mbps=83.9" {
		t.Fatalf("bulk line %q", bulk)
	}
}

// TestDownloadsAreOffTheSteal: no wire's 1 MiB is near :x0 or :x5 (the
// neighbours' steal, opyt 1.1) nor in the hub's round (cascadewatch
// RoundAt 1:15 to about 1:55, the points' protocol tasks about 3:00–4:10),
// and no two wires take bytes in the same 15 s.
func TestDownloadsAreOffTheSteal(t *testing.T) {
	busy := [][2]time.Duration{
		{9*time.Minute + 30*time.Second, 10*time.Minute + 45*time.Second},
		{-30 * time.Second, 45 * time.Second},
		{4*time.Minute + 30*time.Second, 5*time.Minute + 45*time.Second},
		{75 * time.Second, 2 * time.Minute},
		{3 * time.Minute, 4*time.Minute + 15*time.Second},
	}
	var ats []time.Duration
	for wire, at := range downloadAt {
		for _, b := range busy {
			if at >= b[0] && at <= b[1] {
				t.Fatalf("%s's 1 MiB at %s is in %s–%s", wire, at, b[0], b[1])
			}
		}
		for _, o := range ats {
			if d := at - o; d > -15*time.Second && d < 15*time.Second {
				t.Fatalf("two wires' 1 MiB %s apart", d)
			}
		}
		ats = append(ats, at)
	}
}

// TestTheGoEntryLeavesChromiumsQuicWireOut: a file with the pipe's wire
// h3dgram reads, its usage checked, and the Go entry builds only the wires
// it carries.
func TestTheGoEntryLeavesChromiumsQuicWireOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wires.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"steer": true, "wires": [{"name": "udp", "kind": "udp", "port": 20447, "members": 2},
		{"name": "h3dgram", "kind": "h3dgram", "port": 4445, "members": 4, "usage": "proxy"}]}`)
	f, _, err := readWires(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Wires) != 2 || f.Wires[1].Usage != "proxy" {
		t.Fatalf("read %+v", f.Wires)
	}
	ws, err := buildWires(Config{Exit: "127.0.0.1:443", Key: "k"}, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].ID != wireIDs["udp"] {
		t.Fatalf("the Go entry built %d wires, want the udp one alone", len(ws))
	}
	for _, bad := range []string{
		`{"wires": [{"name": "h3dgram", "kind": "h3dgram", "port": 4445, "members": 4, "usage": "kProxy"}]}`,
		`{"wires": [{"name": "udp", "kind": "udp", "port": 20447, "members": 2, "usage": "proxy"}]}`,
	} {
		write(bad)
		if _, _, err := readWires(path); err == nil {
			t.Fatalf("taken: %s", bad)
		}
	}
}
