package main

// receivers_linux.go: the entry over hk-pipe holds the trunk to more than one
// receiver abroad and moves off a dead one by itself (hikkari-client
// plans/2026-11-16-second-receiver.md, P4). The receivers of a group are one
// relay for a client (its domain, its keys) at other addresses on the same
// ports: EDGE_TRUNK_EXIT is the first, EDGE_TRUNK_RECEIVERS the others. The
// pipe numbers them (rx) for one generation: the one this generation starts
// on is 0, the rest follow in the env's order. Which one is active outlives
// a reload: the brain hands its address to the next generation
// (envActiveReceiver), which starts on it.
//
// A move is the minute line's event for the hub (cascade watch):
//
//	nekko-trunk-entry: trunk: receiver moved from <ip> to <ip>: <why>
//
// The hub moves the entry by hand (P5) through a file beside the wires file
// (receiverFilePath): when it changes to another receiver, one that is
// heard, the entry moves there (why "by hand"); one not heard is refused,
// since a move cuts every client. Once a minute the brain says how each
// receiver stands:
//
//	nekko-trunk-entry: trunk: receivers <ip> active up=N proof_ms=M; <ip> standby up=N proof_ms=M
//
// proof_ms is how long ago one of its wires last heard the exit, -1 never.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// setReceivers gives the pipe every receiver but the one it starts on, on
// standby.
func (b *brain) setReceivers() error {
	for rx, ip := range b.order {
		if rx == 0 {
			continue
		}
		if err := b.pipe.SetReceiver(byte(rx), ip, b.c.ReceiverPool); err != nil {
			return fmt.Errorf("receiver %s: %w", ip, err)
		}
	}
	return nil
}

// runReceivers moves the entry off a dead receiver (pipectl.RunReceivers)
// and keeps what follows from it: the wires the hub's file gives later dial
// the active receiver, and a reload starts on it.
func (b *brain) runReceivers(ctx context.Context) {
	if len(b.order) < 2 {
		return
	}
	at := map[byte]netip.Addr{}
	for rx, ip := range b.order {
		at[byte(rx)] = ip
	}
	go b.followReceiverFile(ctx, at)
	go b.sayReceivers(ctx, at)
	b.pipe.RunReceivers(ctx, at, func(m pipectl.Move) {
		if m.Err != nil {
			log.Printf("nekko-trunk-entry: trunk: receiver move from %s to %s failed: %v", m.FromIP, m.ToIP, m.Err)
			return
		}
		b.setActive(m.ToIP)
		log.Printf("nekko-trunk-entry: trunk: receiver moved from %s to %s: %s", m.FromIP, m.ToIP, m.Why)
	})
}

// setActive makes ip the active receiver's address for what the brain
// dials after.
func (b *brain) setActive(ip netip.Addr) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.exit = netip.AddrPortFrom(ip, b.exit.Port())
}

// exitNow is the active receiver's relay: the pool's end, the wires' host.
func (b *brain) exitNow() netip.AddrPort {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exit
}

// moveMu keeps a move by hand and the judge's apart in the brain's books:
// the pipe takes either, one at a time.
var moveMu sync.Mutex

// receiverFileEvery is how often the brain reads the hub's receiver file: a
// move by hand lands within it.
const receiverFileEvery = time.Second

// followReceiverFile moves the entry where the hub's file says each time the
// hub writes it (P5), its bytes the same or not: after the judge moved the
// entry off a receiver, the file still names that one, and the hub naming it
// again is a move back. What the file was when the generation started is
// where the generation started, not a move.
func (b *brain) followReceiverFile(ctx context.Context, at map[byte]netip.Addr) {
	path := receiverFilePath()
	last, lastAt := readReceiverFileStamp(path)
	t := time.NewTicker(receiverFileEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		raw, ip, err := readReceiverFile(path)
		at2 := receiverFileTime(path)
		if bytes.Equal(raw, last) && at2.Equal(lastAt) {
			continue
		}
		last, lastAt = raw, at2
		if err != nil {
			log.Printf("nekko-trunk-entry: trunk: %v (stays where it is)", err)
			continue
		}
		if !ip.IsValid() {
			continue
		}
		if err := b.moveByHand(at, ip); err != nil {
			log.Printf("nekko-trunk-entry: trunk: receiver %s by hand refused: %v", ip, err)
		}
	}
}

// readReceiverFileStamp is the file's bytes and when it was last written.
func readReceiverFileStamp(path string) ([]byte, time.Time) {
	raw, _, _ := readReceiverFile(path)
	return raw, receiverFileTime(path)
}

// receiverFileTime is when the hub last wrote the file (it writes a new one
// and renames it over), zero when there is none.
func receiverFileTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// moveByHand makes the receiver at ip the active one, if it is one of the
// entry's and heard now. The one already active is said, so the hub hears an
// answer to every word.
func (b *brain) moveByHand(at map[byte]netip.Addr, ip netip.Addr) error {
	moveMu.Lock()
	defer moveMu.Unlock()
	from := b.exitNow().Addr()
	if ip == from {
		log.Printf("nekko-trunk-entry: trunk: receiver %s by hand: already the active one", ip)
		return nil
	}
	rx, ok := byte(0), false
	for r, a := range at {
		if a == ip {
			rx, ok = r, true
		}
	}
	if !ok {
		return fmt.Errorf("not one of the entry's receivers %v", b.order)
	}
	rs, rsAt := b.pipe.Receivers()
	if rsAt.IsZero() {
		return fmt.Errorf("the pipe has not said how the receivers stand yet")
	}
	age := time.Since(rsAt)
	heard := false
	for _, r := range rs.Standby {
		if r.Rx == rx {
			heard = receiverHeard(r.Wires, age)
		}
	}
	if !heard {
		return fmt.Errorf("not heard within %s: a move would cut every client into nothing", pipectl.DeadAfter)
	}
	if err := b.pipe.SetActive(rx, ip); err != nil {
		return err
	}
	b.setActive(ip)
	log.Printf("nekko-trunk-entry: trunk: receiver moved from %s to %s: by hand", from, ip)
	return nil
}

// receiverHeard: one of wires, said age ago, heard its exit within
// DeadAfter and has a member up (the judge's rule, pipectl judge.go).
func receiverHeard(wires []pipectl.WireTick, age time.Duration) bool {
	for _, w := range wires {
		if w.Up > 0 && w.ProofAgo >= 0 && w.ProofAgo+age < pipectl.DeadAfter {
			return true
		}
	}
	return false
}

// receiversEvery is how often the brain says how the receivers stand.
const receiversEvery = time.Minute

// sayReceivers says once a minute how each receiver stands: members up and
// the freshest proof of its wires.
func (b *brain) sayReceivers(ctx context.Context, at map[byte]netip.Addr) {
	t := time.NewTicker(receiversEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rs, rsAt := b.pipe.Receivers()
		tick, tickAt := b.pipe.Tick()
		if rsAt.IsZero() || tickAt.IsZero() {
			continue
		}
		parts := []string{receiverPart(at[rs.Active], "active", tick.Wires, time.Since(tickAt))}
		for _, r := range rs.Standby {
			ip := r.IP
			if a, ok := at[r.Rx]; ok {
				ip = a
			}
			parts = append(parts, receiverPart(ip, "standby", r.Wires, time.Since(rsAt)))
		}
		log.Printf("nekko-trunk-entry: trunk: receivers %s", strings.Join(parts, "; "))
	}
}

func receiverPart(ip netip.Addr, role string, wires []pipectl.WireTick, age time.Duration) string {
	up, proof := 0, time.Duration(-1)
	for _, w := range wires {
		up += w.Up
		if w.ProofAgo >= 0 && (proof < 0 || w.ProofAgo < proof) {
			proof = w.ProofAgo
		}
	}
	ms := int64(-1)
	if proof >= 0 {
		ms = (proof + age).Milliseconds()
	}
	return fmt.Sprintf("%s %s up=%d proof_ms=%d", ip, role, up, ms)
}
