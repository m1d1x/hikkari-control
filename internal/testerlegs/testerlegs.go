// Package testerlegs is the hub's side of observing testers' paths through
// the cascade leg by leg (hikkari-client
// plans/2026-11-10-hikkari-observability-plan.md, этап 1б; owner's decision
// 10.11: testers are observed in full, other users not at all).
//
// The exit relay marks a tester's stream or flow on the trunk; both ends then
// report it, under one observation id, at the mark and at its end:
//
//	exit relay pulse  "legs": [row, ...] (with the key)        ─┐
//	entry journal     "nekko-trunk-entry: observed id=… …"      ─┴─► tester_legs (ClickHouse)
//
// The exit's row says whose it is (the key, filed as its account and never
// stored); the entry's never does. A reader joins them by id: the entry's
// client_* numbers are the leg client → entry, the trunk's numbers at each end
// the leg entry → exit, the relay's v1.04 periods (hikkari_obs, path entry)
// what the session saw beyond.
package testerlegs

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sides of a leg row.
const (
	SideEntry = "entry"
	SideExit  = "exit"
)

// Row is one report of one end of an observed stream or flow.
type Row struct {
	TS      time.Time
	ID      string // the observation's, 16 hex
	Side    string
	Box     string // the entry's box, or the exit's relay
	Subject string // the account (exit rows only)
	Kind    string // stream, flow
	Phase   string // mark, end
	Port    uint16
	AgeMs   int64
	Up      int64
	Down    int64
	Chunks  int64
	Resent  int64
	// Carriers and Deaths: members that carried the stream's chunks, and
	// that died owing them.
	Carriers  uint32
	Deaths    uint32
	MaxWaitMs int64
	DgOut     int64
	DgIn      int64
	DgCopy    int64
	End       string
	// The client's TCP as the entry's kernel saw it (entry rows of streams
	// on Linux); HaveTCP false when not read.
	HaveTCP       bool
	ClientRTTMs   float64
	ClientRTTVar  float64
	ClientRetrans int64
	ClientLost    int64
	ClientUnacked int64
	ClientCwnd    int64
	ClientSegsOut int64
	ClientSegsIn  int64
}

// Sink takes rows (analytics.Store).
type Sink interface {
	InsertTesterLegs(ctx context.Context, rows []Row) error
}

// Accounts names the account behind each access key (subscription.Service).
type Accounts interface {
	AccountsOf(ctx context.Context, keys []string) (map[string]string, error)
}

// Recorder files rows from exits and entries.
type Recorder struct {
	Sink     Sink
	Accounts Accounts
}

// exitRow is the exit's wire shape, shared by contract with
// hikkari-control edge/trunk_observe.go (legRow).
type exitRow struct {
	ID        string `json:"id"`
	Side      string `json:"side"`
	Kind      string `json:"kind"`
	Phase     string `json:"phase"`
	Key       string `json:"key"`
	AtMs      int64  `json:"atMs"`
	Port      int    `json:"port"`
	AgeMs     int64  `json:"ageMs"`
	Up        int64  `json:"up"`
	Down      int64  `json:"down"`
	Chunks    int64  `json:"chunks"`
	Resent    int64  `json:"resent"`
	Carriers  int    `json:"carriers"`
	Deaths    int    `json:"deaths"`
	MaxWaitMs int64  `json:"maxWaitMs"`
	DgOut     int64  `json:"dgOut"`
	DgIn      int64  `json:"dgIn"`
	DgCopy    int64  `json:"dgCopy"`
	End       string `json:"end"`
}

// maxRows bounds what one pulse files: the edge keeps at most 4096.
const maxRows = 4096

var (
	idRe   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	wordRe = regexp.MustCompile(`^[a-z_]{1,16}$`)
)

// RecordExit files a relay's rows from its pulse. A row of a key the hub
// does not know is dropped: it was nobody's, or a tester gone since.
func (r *Recorder) RecordExit(ctx context.Context, relay string, raw []json.RawMessage, now time.Time) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxRows {
		raw = raw[len(raw)-maxRows:]
	}
	rows := make([]exitRow, 0, len(raw))
	keys := make([]string, 0, len(raw))
	for _, m := range raw {
		var e exitRow
		if err := json.Unmarshal(m, &e); err != nil || !idRe.MatchString(e.ID) || !wordRe.MatchString(e.Kind) ||
			!wordRe.MatchString(e.Phase) || e.Key == "" || (e.End != "" && !wordRe.MatchString(e.End)) {
			continue
		}
		rows = append(rows, e)
		keys = append(keys, e.Key)
	}
	accounts, err := r.Accounts.AccountsOf(ctx, keys)
	if err != nil {
		return err
	}
	out := make([]Row, 0, len(rows))
	for _, e := range rows {
		account := accounts[e.Key]
		if account == "" {
			continue
		}
		ts := time.UnixMilli(e.AtMs).UTC()
		if e.AtMs <= 0 || ts.After(now.Add(time.Minute)) || ts.Before(now.Add(-24*time.Hour)) {
			ts = now
		}
		out = append(out, Row{
			TS: ts, ID: e.ID, Side: SideExit, Box: relay, Subject: account, Kind: e.Kind, Phase: e.Phase,
			Port: uint16(e.Port), AgeMs: e.AgeMs, Up: e.Up, Down: e.Down, Chunks: e.Chunks, Resent: e.Resent,
			Carriers: uint32(max(e.Carriers, 0)), Deaths: uint32(max(e.Deaths, 0)), MaxWaitMs: e.MaxWaitMs,
			DgOut: e.DgOut, DgIn: e.DgIn, DgCopy: e.DgCopy, End: e.End,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return r.Sink.InsertTesterLegs(ctx, out)
}

// RecordEntry files rows read from an entry's journal (ParseEntryLine).
func (r *Recorder) RecordEntry(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	return r.Sink.InsertTesterLegs(ctx, rows)
}

// entryPrefix is how the entry's line starts after the logger's own time
// (hikkari-control trunkentry/observe.go).
const entryPrefix = "nekko-trunk-entry: observed "

// IsEntryLine says whether a journal message is an entry's observed line.
func IsEntryLine(msg string) bool { return strings.Contains(msg, entryPrefix) }

// ParseEntryLine reads one entry's observed line, logged on box at ts.
func ParseEntryLine(box string, ts time.Time, msg string) (Row, error) {
	_, rest, ok := strings.Cut(msg, entryPrefix)
	if !ok {
		return Row{}, fmt.Errorf("testerlegs: not an observed line")
	}
	f := map[string]string{}
	for _, kv := range strings.Fields(rest) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			f[k] = v
		}
	}
	row := Row{TS: ts.UTC(), ID: f["id"], Side: SideEntry, Box: box, Kind: f["kind"], Phase: f["phase"], End: f["end"]}
	if row.End == "-" {
		row.End = ""
	}
	if !idRe.MatchString(row.ID) || !wordRe.MatchString(row.Kind) || !wordRe.MatchString(row.Phase) ||
		(row.End != "" && !wordRe.MatchString(row.End)) {
		return Row{}, fmt.Errorf("testerlegs: observed line: id %q kind %q phase %q", row.ID, row.Kind, row.Phase)
	}
	i := func(k string) int64 { n, _ := strconv.ParseInt(f[k], 10, 64); return n }
	fl := func(k string) float64 { n, _ := strconv.ParseFloat(f[k], 64); return n }
	row.Port = uint16(i("port"))
	row.AgeMs, row.Up, row.Down, row.Chunks, row.Resent = i("age_ms"), i("up"), i("down"), i("chunks"), i("resent")
	row.Carriers, row.Deaths, row.MaxWaitMs = uint32(max(i("carriers"), 0)), uint32(max(i("deaths"), 0)), i("max_wait_ms")
	if o, in, ok := strings.Cut(f["dgrams"], "/"); ok {
		row.DgOut, _ = strconv.ParseInt(o, 10, 64)
		row.DgIn, _ = strconv.ParseInt(in, 10, 64)
	}
	row.DgCopy = i("copies")
	if _, ok := f["client_rtt_ms"]; ok {
		row.HaveTCP = true
		row.ClientRTTMs, row.ClientRTTVar = fl("client_rtt_ms"), fl("client_rttvar_ms")
		row.ClientRetrans, row.ClientLost, row.ClientUnacked, row.ClientCwnd = i("client_retrans"), i("client_lost"), i("client_unacked"), i("client_cwnd")
		if o, in, ok := strings.Cut(f["client_segs"], "/"); ok {
			row.ClientSegsOut, _ = strconv.ParseInt(o, 10, 64)
			row.ClientSegsIn, _ = strconv.ParseInt(in, 10, 64)
		}
	}
	return row, nil
}
