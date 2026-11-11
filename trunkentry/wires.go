package main

// The trunk's other wires on the entry (hikkari-client
// plans/2026-11-11-trunk-wires-stage4.md, шаги 3–6). The hub writes the file
// (hikkari-control internal/entry/trunkwires.go,
// `entry-box trunk-entry-wires`):
//
//	{"steer": true, "wires": [{"name": "udp", "kind": "udp", "port": 20447, "members": 2},
//	                          {"name": "quic", "kind": "quic", "port": 20448, "members": 4}]}
//
// and the running entry reads it every 10 s: a wire added comes up, a wire
// gone has its members closed, a wire changed is dialled anew (trunk
// Entry.SetWires), all without a new process. No file, or no wire in it, is
// the entry as it was: the pool alone, every class on any member.
//
// With wires, the newest generation measures every wire, the pool too, all
// the time (stage plan, раздел 5): the trunk's call held to the wire (a line
// a minute, `call wire=<name>`) and 1 MiB every 10 minutes at the wire's own
// minute (`bulk wire=<name>`), and gives them to the selector. With steer,
// the selector sets the route every second: voice and bulk each on the wire
// its numbers rank first, off a red wire at once. Without steer the route is
// any member, and the numbers are only measured.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/quicwire"
	"github.com/m1d1x/hikkari-protocol/trunk/udpwire"
)

// defaultWiresFile is where the hub writes the wires (EDGE_TRUNK_WIRES_FILE
// moves it, for tests).
const defaultWiresFile = "/etc/nekko-trunk-entry.wires.json"

// wiresEvery is how often the file is read.
var wiresEvery = 10 * time.Second

type wireSpec struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Port    int    `json:"port"`
	Members int    `json:"members"`
}

type wiresSpec struct {
	Steer bool       `json:"steer"`
	Wires []wireSpec `json:"wires"`
}

// wireIDs are the wires' ids on the trunk: fixed per kind, so the exit's
// numbers and the logs say the same wire across processes.
var wireIDs = map[string]trunk.WireID{"quic": 1, "salamander": 2, "udp": 3}

// downloadAt is each wire's second in every ten minutes for its 1 MiB: none
// at :x0 (the hub's rounds) nor at the stand's (edge/wirestand.go standSlot,
// 1:00, 3:30, 6:00, 8:30) while it runs beside.
var downloadAt = map[string]time.Duration{
	"pool":       2*time.Minute + 15*time.Second,
	"quic":       4*time.Minute + 45*time.Second,
	"salamander": 7*time.Minute + 15*time.Second,
}

// downloadSize and downloadLimit are the 1 MiB and how long it may take.
const (
	downloadSize  = 1 << 20
	downloadLimit = 60 * time.Second
)

func wiresFilePath() string {
	if p := os.Getenv("EDGE_TRUNK_WIRES_FILE"); p != "" {
		return p
	}
	return defaultWiresFile
}

// readWires reads the file; a missing one is no wire.
func readWires(path string) (wiresSpec, []byte, error) {
	var f wiresSpec
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil, nil
	}
	if err != nil {
		return f, nil, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, raw, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, w := range f.Wires {
		if _, ok := wireIDs[w.Kind]; !ok || w.Name != w.Kind {
			return f, raw, fmt.Errorf("%s: wire %q of kind %q", path, w.Name, w.Kind)
		}
		if seen[w.Kind] || w.Port < 1 || w.Port > 65535 || w.Members < 1 || w.Members > 32 {
			return f, raw, fmt.Errorf("%s: wire %q: twice, or port %d, or members %d", path, w.Name, w.Port, w.Members)
		}
		seen[w.Kind] = true
	}
	return f, raw, nil
}

// buildWires turns the file into the trunk's wires, dialled to the exit's
// address on each wire's port with the trunk key.
func buildWires(c Config, f wiresSpec, udpPorts *trunk.PortBook) ([]trunk.Wire, error) {
	host, _, err := net.SplitHostPort(c.Exit)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, err
	}
	out := make([]trunk.Wire, 0, len(f.Wires))
	for _, w := range f.Wires {
		at := netip.AddrPortFrom(ip, uint16(w.Port))
		tw := trunk.Wire{ID: wireIDs[w.Kind], Name: w.Name, Members: w.Members, Ports: udpPorts, Knobs: fmt.Sprintf("port=%d", w.Port)}
		switch w.Kind {
		case "udp":
			tw.Shape, tw.Packet = "random", true
			tw.Dial = udpwire.Dialer{Exit: at, Key: c.Key}.Dial
		case "quic", "salamander":
			tw.Shape, tw.Streams = "browser", true
			if w.Kind == "salamander" {
				tw.Shape = "random"
			}
			tw.Dial = quicwire.Dialer{Exit: at, Key: c.Key, SNI: c.SNI, Salamander: w.Kind == "salamander"}.Dial
		}
		out = append(out, tw)
	}
	return out, nil
}

// poolWire is the pool as the selector sees it.
func poolWire() trunk.Wire {
	return trunk.Wire{ID: trunk.PoolWire, Name: "pool", Shape: "browser", Streams: true}
}

// wireUDPPorts is the book of the wires' UDP source ports: the pool's range,
// another protocol.
func wireUDPPorts(c Config) (*trunk.PortBook, error) {
	return trunk.NewPortBook(c.PortLo, c.PortHi, 0)
}

// addWires gives a new generation's entry the wires of the file before it
// runs, so it dials them from its start.
func addWires(c Config, entry *trunk.Entry) error {
	f, _, err := readWires(wiresFilePath())
	if err != nil {
		return err
	}
	ports, err := wireUDPPorts(c)
	if err != nil {
		return err
	}
	ws, err := buildWires(c, f, ports)
	if err != nil {
		return err
	}
	for _, w := range ws {
		if err := entry.AddWire(w); err != nil {
			return err
		}
	}
	return nil
}

// runWires follows the file until ctx ends: the newest generation's.
func runWires(ctx context.Context, c Config, entry *trunk.Entry) {
	ports, err := wireUDPPorts(c)
	if err != nil {
		log.Printf("nekko-trunk-entry: wires: %v", err)
		return
	}
	path := wiresFilePath()
	var last []byte
	first := true
	stop := func() {}
	defer func() { stop() }()
	t := time.NewTicker(wiresEvery)
	defer t.Stop()
	for {
		f, raw, err := readWires(path)
		switch {
		case err != nil:
			if !bytes.Equal(raw, last) {
				log.Printf("nekko-trunk-entry: wires: %v (kept what runs)", err)
				last = raw
			}
		case first || !bytes.Equal(raw, last):
			last = raw
			ws, err := buildWires(c, f, ports)
			if err == nil {
				// The generation came up with the file's wires (addWires):
				// the same ones restart nothing.
				err = entry.SetWires(ws)
			}
			if err != nil {
				log.Printf("nekko-trunk-entry: wires: %v (kept what runs)", err)
				break
			}
			first = false
			stop()
			wctx, cancel := context.WithCancel(ctx)
			stop = cancel
			names := make([]string, 0, len(ws))
			for _, w := range ws {
				names = append(names, fmt.Sprintf("%s:%d", w.Name, w.Members))
			}
			if len(names) == 0 {
				names = append(names, "-")
			}
			log.Printf("nekko-trunk-entry: wires %s steer=%v", strings.Join(names, ","), f.Steer)
			measure(wctx, entry, ws, f.Steer)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// measure runs every wire's call and downloads, and the selector over them,
// until ctx ends. With no wire it measures nothing and sets no route.
func measure(ctx context.Context, entry *trunk.Entry, ws []trunk.Wire, steer bool) {
	if len(ws) == 0 {
		entry.SetRoute(trunk.AnyRoute)
		return
	}
	all := append([]trunk.Wire{poolWire()}, ws...)
	sel := trunk.NewSelector(trunk.SelectorConfig{}, all...)
	for _, w := range all {
		go trunk.RunCall(ctx, entry.Session, trunk.CallConfig{Copies: 1, OnWire: true, Wire: w.ID}, func(cw trunk.CallWindow) {
			sel.Call(w.ID, cw)
			log.Print("nekko-trunk-entry: " + wireCallLine(w.Name, cw))
		})
		if at, ok := downloadAt[w.Name]; ok && w.Streams {
			go downloads(ctx, entry, sel, w, at)
		}
	}
	if steer {
		go entry.Steer(ctx, sel, func(format string, a ...any) { log.Printf("nekko-trunk-entry: "+format, a...) })
	} else {
		entry.SetRoute(trunk.AnyRoute)
	}
}

// downloads takes 1 MiB on w at its second of every ten minutes.
func downloads(ctx context.Context, entry *trunk.Entry, sel *trunk.Selector, w trunk.Wire, at time.Duration) {
	for {
		now := time.Now()
		next := now.Truncate(10 * time.Minute).Add(at)
		if !next.After(now) {
			next = next.Add(10 * time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		dctx, cancel := context.WithTimeout(ctx, downloadLimit)
		r := entry.Download(dctx, w.ID, downloadSize)
		cancel()
		if ctx.Err() != nil {
			return
		}
		sel.Download(w.ID, trunk.Download{At: next, OK: r.End == "ok", Took: r.Took, Bytes: int64(r.Got)})
		log.Print("nekko-trunk-entry: " + wireBulkLine(w.Name, next, r))
	}
}

// wireCallLine is a wire's call minute: the trunk call's line with the wire
// named first. The hub reads it as the stand's (internal/cascadewatch
// wire.go), under target trunk-<wire>.
func wireCallLine(wire string, w trunk.CallWindow) string {
	return "call wire=" + wire + " " + strings.TrimPrefix(callLine(w), "call ")
}

// wireBulkLine is a wire's 1 MiB, in the stand's keys (edge/wirestand.go
// bulkLine).
func wireBulkLine(wire string, from time.Time, r trunk.DownloadResult) string {
	mbps := 0.0
	if r.Took > 0 {
		mbps = float64(r.Got*8) / r.Took.Seconds() / 1e6
	}
	return fmt.Sprintf("bulk wire=%s kind=1m from=%d bytes=%d want=%d ms=%d end=%s mbps=%.1f",
		wire, from.Unix(), r.Got, r.Want, r.Took.Milliseconds(), r.End, mbps)
}
