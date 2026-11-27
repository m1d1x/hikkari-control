package main

// points.go: how the border points stand, as the brain says them to the hub
// (hikkari-client plans/2026-11-19-border-forwarders.md, G5). The pipe says
// after each tick, per point, the members of the current session at it
// (member i of every wire and pool at point i mod n), how many are up and
// how long ago one of them last proved the core (pipectl.PointTick). A point
// with members and none of them up is silent: the entry reaches the core
// through it no more (the point dead, frozen, or cut on its way). No number
// of our own decides it: a member is down by its own health, as the pipe
// judges every member.
//
// The brain says a change at once and the minute once a minute:
//
//	nekko-trunk-entry: trunk: point <ip> silent: 0 of N members up
//	nekko-trunk-entry: trunk: point <ip> back: U of N members up after S s
//	nekko-trunk-entry: trunk: every point silent
//	nekko-trunk-entry: trunk: a point back after S s of every point silent
//	nekko-trunk-entry: trunk: points from=<unix> <ip> up=U/N proof_ms=P silent_s=S; ...
//
// silent_s is the seconds of the minute the point was silent, by the pipe's
// word once a second; proof_ms -1 is none ever.

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// pointBook keeps the points between the pipe's words: since when each is
// silent, its silent seconds this minute, its latest word, and since when
// every point is.
type pointBook struct {
	order    []netip.Addr
	since    map[netip.Addr]time.Time
	silentS  map[netip.Addr]int
	last     map[netip.Addr]pipectl.PointTick
	allSince time.Time
}

func newPointBook() *pointBook {
	return &pointBook{since: map[netip.Addr]time.Time{}, silentS: map[netip.Addr]int{}, last: map[netip.Addr]pipectl.PointTick{}}
}

// silent is whether the pipe's word says the point carries nothing.
func silent(p pipectl.PointTick) bool { return p.Members > 0 && p.Up == 0 }

// step takes the pipe's word at now (a second after the last) and returns
// the lines of what changed. Points no longer given are forgotten without a
// word: the hub changed them.
func (b *pointBook) step(now time.Time, ps []pipectl.PointTick) []string {
	var out []string
	seen := map[netip.Addr]bool{}
	b.order = b.order[:0]
	all := len(ps) > 0
	for _, p := range ps {
		seen[p.IP] = true
		b.order = append(b.order, p.IP)
		b.last[p.IP] = p
		was, wasSilent := b.since[p.IP]
		if silent(p) {
			b.silentS[p.IP]++
			if !wasSilent {
				b.since[p.IP] = now
				out = append(out, fmt.Sprintf("trunk: point %s silent: 0 of %d members up", p.IP, p.Members))
			}
			continue
		}
		all = false
		if wasSilent {
			delete(b.since, p.IP)
			out = append(out, fmt.Sprintf("trunk: point %s back: %d of %d members up after %d s", p.IP, p.Up, p.Members, secs(now.Sub(was))))
		}
	}
	for ip := range b.last {
		if !seen[ip] {
			delete(b.last, ip)
			delete(b.since, ip)
			delete(b.silentS, ip)
		}
	}
	switch {
	case all && b.allSince.IsZero():
		b.allSince = now
		out = append(out, "trunk: every point silent")
	case !all && !b.allSince.IsZero():
		if len(ps) > 0 {
			out = append(out, fmt.Sprintf("trunk: a point back after %d s of every point silent", secs(now.Sub(b.allSince))))
		}
		b.allSince = time.Time{}
	}
	return out
}

// minute is the minute's line from from, "" with no points; it starts the
// next minute's silent seconds.
func (b *pointBook) minute(from time.Time) string {
	if len(b.order) == 0 {
		return ""
	}
	parts := make([]string, 0, len(b.order))
	for _, ip := range b.order {
		p := b.last[ip]
		ms := int64(-1)
		if p.ProofAgo >= 0 {
			ms = p.ProofAgo.Milliseconds()
		}
		parts = append(parts, fmt.Sprintf("%s up=%d/%d proof_ms=%d silent_s=%d", ip, p.Up, p.Members, ms, b.silentS[ip]))
		b.silentS[ip] = 0
	}
	return fmt.Sprintf("trunk: points from=%d %s", from.Unix(), strings.Join(parts, "; "))
}

// secs is d in whole seconds, at least 1: a silence the brain saw was.
func secs(d time.Duration) int64 {
	return max(1, int64((d+time.Second/2)/time.Second))
}

// pointChangeLines are the lines of a change of the path the point judge
// made (runPoints).
func pointChangeLines(c pipectl.PointChange) []string {
	path := make([]string, len(c.Path))
	for i, ip := range c.Path {
		path[i] = ip.String()
	}
	tail := fmt.Sprintf("%s; path %s", c.Why, strings.Join(path, " "))
	if c.Err != nil {
		tail += fmt.Sprintf(" (the pipe: %v)", c.Err)
	}
	var out []string
	for _, ip := range c.Left {
		out = append(out, fmt.Sprintf("trunk: point %s off the path: %s", ip, tail))
	}
	for _, ip := range c.Back {
		out = append(out, fmt.Sprintf("trunk: point %s back on the path: %s", ip, tail))
	}
	return out
}
