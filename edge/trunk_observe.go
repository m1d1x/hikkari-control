package main

// A tester's path leg by leg through the cascade (hikkari-client
// plans/2026-11-10-hikkari-observability-plan.md, этап 1б; owner's decision
// 10.11: testers are observed in full, other users not at all).
//
// The client's TLS crosses the entry to this relay, so only here is its key
// known. The trunk's exit dials this relay on loopback for every stream and
// flow it carries (trunkLocal), and remembers which stream each loopback
// connection is (trunkTargets). When the relay admits a tester's key on such
// a connection (the hub names testers' keys in its pulse answer), the exit
// marks the stream (trunk.Session.Observe): the entry reports it too, under
// the same observation id, without learning the key. The exit's reports go
// to the hub in the pulse (legs), its relay's own numbers for the key in the
// v1.04 book as before.
//
// Nothing is marked, kept or sent for a key the hub did not name.

import (
	"encoding/hex"
	"log"
	"net"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

// trunkTargets maps a loopback connection the exit dialled to the stream
// or flow it carries, by network and the connection's local address (which
// the relay sees as the remote).
type trunkTargets struct {
	mu sync.Mutex
	m  map[string]trunk.Target
}

func newTrunkTargets() *trunkTargets { return &trunkTargets{m: map[string]trunk.Target{}} }

func (t *trunkTargets) get(network, remote string) (trunk.Target, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tg, ok := t.m[network+" "+remote]
	return tg, ok
}

// track remembers c as tg's until c is closed.
func (t *trunkTargets) track(network string, c net.Conn, tg trunk.Target) net.Conn {
	key := network + " " + c.LocalAddr().String()
	t.mu.Lock()
	t.m[key] = tg
	t.mu.Unlock()
	return &trackedConn{Conn: c, forget: func() {
		t.mu.Lock()
		if t.m[key] == tg {
			delete(t.m, key)
		}
		t.mu.Unlock()
	}}
}

func (t *trunkTargets) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

type trackedConn struct {
	net.Conn
	forget func()
	once   sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(c.forget)
	return c.Conn.Close()
}

// CloseWrite passes the stream's half-close through to the relay.
func (c *trackedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// setObserved takes the hub's list of testers' keys.
func (n *naiveRuntime) setObserved(keys []string) {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	n.mu.Lock()
	n.observed = want
	n.mu.Unlock()
}

// observeTrunk marks the trunk's stream or flow that the relay's connection
// from remote came by, when key is a tester's. network is "tcp" (the Naive
// channel) or "udp" (the datagram channel).
func (n *naiveRuntime) observeTrunk(network, remote, key string) {
	if key == "" || n.targets == nil {
		return
	}
	n.mu.Lock()
	on := n.observed[key]
	n.mu.Unlock()
	if !on {
		return
	}
	if tg, ok := n.targets.get(network, remote); ok {
		tg.Session.Observe(tg.Kind, tg.ID, key)
	}
}

// legRow is one report of one end of an observed stream or flow, shared by
// contract with the hub (internal/testerlegs): the exit's come in its pulse,
// the entry's from its journal.
type legRow struct {
	ID        string `json:"id"`
	Side      string `json:"side"`
	Kind      string `json:"kind"`
	Phase     string `json:"phase"`
	Key       string `json:"key,omitempty"`
	AtMs      int64  `json:"atMs"`
	Port      int    `json:"port"`
	AgeMs     int64  `json:"ageMs"`
	Up        int64  `json:"up,omitempty"`
	Down      int64  `json:"down,omitempty"`
	Chunks    int64  `json:"chunks,omitempty"`
	Resent    int64  `json:"resent,omitempty"`
	Carriers  int    `json:"carriers,omitempty"`
	Deaths    int    `json:"deaths,omitempty"`
	MaxWaitMs int64  `json:"maxWaitMs,omitempty"`
	DgOut     int64  `json:"dgOut,omitempty"`
	DgIn      int64  `json:"dgIn,omitempty"`
	DgCopy    int64  `json:"dgCopy,omitempty"`
	End       string `json:"end,omitempty"`
}

// maxLegs bounds the rows waiting for a pulse; past it the oldest go.
const maxLegs = 4096

func exitLeg(r trunk.Report, at time.Time) legRow {
	var id [8]byte
	for i := 0; i < 8; i++ {
		id[i] = byte(r.ID >> (56 - 8*i))
	}
	return legRow{
		ID: hex.EncodeToString(id[:]), Side: "exit", Kind: r.Kind.String(), Phase: r.Phase, Key: r.Tag,
		AtMs: at.UnixMilli(), Port: int(r.Port), AgeMs: r.Age.Milliseconds(),
		Up: r.Up, Down: r.Down, Chunks: r.Chunks, Resent: r.Resent, Carriers: r.Carriers, Deaths: r.Deaths,
		MaxWaitMs: r.MaxWait.Milliseconds(), DgOut: r.DgramsOut, DgIn: r.DgramsIn, DgCopy: r.DgramsCopy, End: r.End,
	}
}

// trunkObserved queues the exit's report for the next pulse.
func (n *naiveRuntime) trunkObserved(r trunk.Report) {
	row := exitLeg(r, time.Now())
	log.Printf("nekko-edge: trunk observed %s %s %s up=%d down=%d resent=%d deaths=%d max_wait_ms=%d dgrams=%d/%d end=%s",
		row.ID, row.Kind, row.Phase, row.Up, row.Down, row.Resent, row.Deaths, row.MaxWaitMs, row.DgOut, row.DgIn, row.End)
	n.mu.Lock()
	n.legs = append(n.legs, row)
	if over := len(n.legs) - maxLegs; over > 0 {
		n.legs = append([]legRow(nil), n.legs[over:]...)
	}
	n.mu.Unlock()
}

// takeLegs hands the queued rows to a pulse; legsSent puts back those a
// pulse did not deliver.
func (n *naiveRuntime) takeLegs() []legRow {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.legs
	n.legs = nil
	return out
}

func (n *naiveRuntime) legsSent(rows []legRow, ok bool) {
	if ok || len(rows) == 0 {
		return
	}
	n.mu.Lock()
	n.legs = append(rows, n.legs...)
	if over := len(n.legs) - maxLegs; over > 0 {
		n.legs = append([]legRow(nil), n.legs[over:]...)
	}
	n.mu.Unlock()
}
