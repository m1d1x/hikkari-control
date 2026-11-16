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

import (
	"context"
	"fmt"
	"log"
	"net/netip"

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
