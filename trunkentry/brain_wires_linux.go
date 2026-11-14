package main

// brain_wires_linux.go: the brain's wires over hk-pipe (roadmap 4.4; stage
// plan hikkari-client plans/2026-11-11-trunk-wires-stage4.md, раздел 4.4).
// The hub's file (wires.go) is read at the brain's start and then every
// wiresEvery by the newest generation, as the Go entry's runWires does: a
// wire added is given to the pipe, a wire gone is taken from it, a wire
// whose members, port or usage changed is given again (pipectl.SetWire,
// RemoveWire; the pipe keeps its flows: their next datagram goes by the
// route), all in the same process and the same pipe. The selector and the
// calls on the wires go on through a change that leaves the same wires; a
// change of the wires themselves, of steer or of voice starts them anew.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/netip"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// pipeWires are the packet wires of the hub's file the pipe carries: udp,
// and Chromium's QUIC wire h3dgram (R6.3: the pipe opens its members from
// the pool's book of ports, through the libcronet beside it, to the exit's
// QUIC port, whose certificate names the trunk's SNI). quic and salamander
// are not carried over to Rust (the owner's 12.11).
func pipeWires(c Config, exit netip.AddrPort, roots string) ([]pipectl.Wire, wiresSpec, error) {
	f, _, err := readWires(wiresFilePath())
	if err != nil {
		return nil, f, err
	}
	return pipeWiresOf(c, exit, roots, f), f, nil
}

// pipeWiresOf is pipeWires of a file already read.
func pipeWiresOf(c Config, exit netip.AddrPort, roots string, f wiresSpec) []pipectl.Wire {
	var out []pipectl.Wire
	for _, w := range f.Wires {
		pw := pipectl.Wire{ID: wireIDs[w.Kind], Name: w.Name, Key: c.Key, Members: w.Members, Stall: c.Stall,
			Exit: netip.AddrPortFrom(exit.Addr(), uint16(w.Port))}
		switch w.Kind {
		case "udp":
		case "h3dgram":
			pw.H3 = &pipectl.H3Form{SNI: c.SNI, Cronet: cronetBeside(c.Pipe), Roots: roots, Usage: w.Usage,
				Ports: [2]uint16{uint16(c.PortLo), uint16(c.PortHi)}}
		default:
			log.Printf("nekko-trunk-entry: wire %s is not carried by the pipe, left out", w.Name)
			continue
		}
		out = append(out, pw)
	}
	return out
}

// wireShape is a pipe wire's shape for the selector's warm pair: Chromium's
// QUIC looks like a browser, the bare UDP wire like nothing.
func wireShape(w pipectl.Wire) string {
	if w.H3 != nil {
		return "browser"
	}
	return "random"
}

// sameWire says whether the pipe already carries w as had: the same
// members, exit and form. Anything else gives it again.
func sameWire(had, w pipectl.Wire) bool {
	if had.Members != w.Members || had.Exit != w.Exit || had.Stall != w.Stall || (had.H3 == nil) != (w.H3 == nil) {
		return false
	}
	return had.H3 == nil || *had.H3 == *w.H3
}

// wireSet is what the selector and the calls stand on: the wires by id and
// shape, steer and voice. Members, ports and usage are not in it.
func wireSet(ws []pipectl.Wire, steer bool, voice trunk.WireID) string {
	parts := make([]string, 0, len(ws)+1)
	parts = append(parts, fmt.Sprintf("steer=%v voice=%d", steer, voice))
	for _, w := range ws {
		parts = append(parts, fmt.Sprintf("%d:%s", w.ID, wireShape(w)))
	}
	return strings.Join(parts, " ")
}

// applyWires gives the pipe the wires of ws in place of b.wires: the route
// leaves a wire before it goes, so no class waits on a wire the pipe has
// not; a wire gone is taken away, a new or changed one given.
func (b *brain) applyWires(ws []pipectl.Wire) error {
	keep := map[trunk.WireID]bool{}
	for _, w := range ws {
		keep[w.ID] = true
	}
	r := b.pipe.Route()
	if r.Voice != trunk.AnyWire && r.Voice != trunk.PoolWire && !keep[r.Voice] {
		r.Voice = trunk.AnyWire
	}
	if r.Bulk != trunk.AnyWire && r.Bulk != trunk.PoolWire && !keep[r.Bulk] {
		r.Bulk = trunk.AnyWire
	}
	if r != b.pipe.Route() {
		b.pipe.SetRoute(r)
	}
	had := map[trunk.WireID]pipectl.Wire{}
	for _, w := range b.wires {
		had[w.ID] = w
		if !keep[w.ID] {
			if err := b.pipe.RemoveWire(w.ID); err != nil {
				return fmt.Errorf("wire %s off: %w", w.Name, err)
			}
		}
	}
	for _, w := range ws {
		if old, ok := had[w.ID]; ok && sameWire(old, w) {
			continue
		}
		if err := b.pipe.SetWire(w); err != nil {
			return fmt.Errorf("wire %s: %w", w.Name, err)
		}
	}
	return nil
}

// measureWires runs the calls on every wire and the pool, and the selector
// over them or the route the file holds, until the returned cancel or ctx.
func (b *brain) measureWires(ctx context.Context) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)
	if len(b.wires) == 0 {
		b.pipe.SetRoute(trunk.AnyRoute)
		return cancel
	}
	carrier := func() trunk.CallCarrier { return b.pipe }
	all := []trunk.Wire{poolWire()}
	ids := []trunk.WireID{trunk.PoolWire}
	names := map[trunk.WireID]string{trunk.PoolWire: "pool"}
	for _, w := range b.wires {
		all = append(all, trunk.Wire{ID: w.ID, Name: w.Name, Shape: wireShape(w), Packet: true})
		ids = append(ids, w.ID)
		names[w.ID] = w.Name
	}
	line := func(id trunk.WireID, cw trunk.CallWindow) {
		log.Print("nekko-trunk-entry: " + wireCallLine(names[id], cw))
	}
	if !b.steer || b.voiceOn != 0 {
		route := trunk.AnyRoute
		if b.voiceOn != 0 {
			route = trunk.Route{Voice: b.voiceOn, Bulk: trunk.PoolWire}
			log.Printf("nekko-trunk-entry: voice held on %s, bulk on the pool, no selector", names[b.voiceOn])
		}
		b.pipe.SetRoute(route)
		for _, id := range ids {
			go trunk.RunCallOn(ctx, carrier, trunk.CallConfig{Copies: 1, OnWire: true, Wire: id}, func(cw trunk.CallWindow) { line(id, cw) })
		}
		return cancel
	}
	sel := trunk.NewSelector(trunk.SelectorConfig{}, all...)
	go b.pipe.Run(ctx, sel, ids, line, func(format string, a ...any) { log.Printf("nekko-trunk-entry: "+format, a...) })
	return cancel
}

// followWires measures the wires the brain came up with, then follows the
// hub's file until ctx ends: the newest generation's.
func (b *brain) followWires(ctx context.Context) {
	path := wiresFilePath()
	_, last, _ := readWires(path)
	failed := ""
	set := wireSet(b.wires, b.steer, b.voiceOn)
	stop := b.measureWires(ctx)
	defer func() { stop() }()
	t := time.NewTicker(wiresEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		f, raw, err := readWires(path)
		if bytes.Equal(raw, last) {
			continue
		}
		if err != nil {
			// A file that cannot be read is not tried again until it changes.
			last = raw
			log.Printf("nekko-trunk-entry: wires: %v (kept what runs)", err)
			continue
		}
		ws := pipeWiresOf(b.c, b.exit, b.roots, f)
		if err := b.applyWires(ws); err != nil {
			// What the pipe took runs; the file is tried again on the next
			// tick, its error said once.
			if msg := err.Error(); msg != failed {
				failed = msg
				log.Printf("nekko-trunk-entry: wires: %v (tried again in %s)", err, wiresEvery)
			}
			continue
		}
		last, failed = raw, ""
		voice := trunk.WireID(0)
		if f.Voice != "" {
			voice = wireIDs[f.Voice]
		}
		b.wires, b.steer, b.voiceOn = ws, f.Steer, voice
		names := make([]string, 0, len(ws))
		for _, w := range ws {
			names = append(names, fmt.Sprintf("%s:%d", w.Name, w.Members))
		}
		if len(names) == 0 {
			names = append(names, "-")
		}
		now := wireSet(ws, f.Steer, voice)
		log.Printf("nekko-trunk-entry: wires %s steer=%v voice=%s, no new process (selector anew: %v)", strings.Join(names, ","), f.Steer, f.Voice, now != set)
		if now != set {
			set = now
			stop()
			stop = b.measureWires(ctx)
		}
	}
}
