// Package testertimeline lays a tester's minutes beside what our side did in
// them (hikkari-client plans/2026-11-18-live-traffic-observability.md, O2).
// A tester saw a lag with their own eyes; the question is whether a rollout,
// a move of the selector, a receiver or an exit epoch, or a profile the
// subscription swapped fell in the same minute. The tester's side comes from
// their log (the client's own lines: stalls, minutes of the tunnel, the voice
// probe, hikkari periods with v1.06 pauses, clicks, hot reloads, health); our
// side from the cascade watch's rows (trunk_runs: the entry's events and the
// people's trunk call). Testers are observed in full by the owner's decision
// of 10.11; nothing here reads an ordinary user.
package testertimeline

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/downtime"
)

// Minute is one minute of the timeline: what the tester saw and what our
// side did. Only minutes with something in them are kept.
type Minute struct {
	At     time.Time `json:"at"`
	Tester *Tester   `json:"tester,omitempty"`
	Ours   []string  `json:"ours,omitempty"`
	// Near are our events within NearBy of a minute in which the tester's
	// tunnel stalled for a second or more, the same minute excluded.
	Near []string `json:"near,omitempty"`
}

// Tester is one minute of the tester's own lines.
type Tester struct {
	// State is the minute of the main metric as the client wrote it: o, i,
	// d, c, n (internal/downtime); empty when the line did not cover it.
	State string `json:"state,omitempty"`
	// Stalls1s and Stalls3s are the tunnel's stalls of at least 1 s and 3 s
	// in the minute, StallMS the length of those over 1 s.
	Stalls1s int   `json:"stalls_1s,omitempty"`
	Stalls3s int   `json:"stalls_3s,omitempty"`
	StallMS  int64 `json:"stall_ms,omitempty"`
	// Voice are the voice probe's series that ended in the minute, as the
	// client wrote their measurement.
	Voice []string `json:"voice,omitempty"`
	// Pauses are the hikkari periods that ended in the minute: the outbound,
	// and per lane the pauses over 60 and 150 ms the client handed out and
	// those a stream copy ended (v1.06).
	Pauses []string `json:"pauses,omitempty"`
	// Events are what the tester did and what the client did on its own:
	// clicks, hot reloads, the auto group's moves, health.
	Events []string `json:"events,omitempty"`
}

// NearBy is how far from a stall our events count as near it.
const NearBy = 2 * time.Minute

// Line is one line of a tester's log with the time it was received at.
type Line struct {
	Text       string
	ReceivedAt time.Time
}

var (
	reLineTS = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?) (?:\[([^\]]+)\] )?(.*)$`)
	reVoice  = regexp.MustCompile(`path=(tun|direct)\b.*?\b(gaps40=\d+ )?gaps60=(\d+) gaps150=(\d+) gap_max=(\d+)`)
	rePeriod = regexp.MustCompile(`outbound/hikkari\[([^\]]+)\]: hikkari period s=\d+ (\{.*\})\s*$`)
)

// clientTime is a log line's time: the client writes its own local time
// without a zone, so offset (local minus UTC) turns it back.
func clientTime(stamp string, offset time.Duration) (time.Time, bool) {
	t, err := time.Parse("2006/01/02 15:04:05.999999", stamp)
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(-offset).UTC(), true
}

// Offset guesses the tester's zone: the median, over chunks, of the last
// line's local time minus the time the chunk was received, rounded to 15
// minutes. A chunk leaves seconds after its lines, so the rounding absorbs
// the delay.
func Offset(chunks [][]Line) time.Duration {
	var seen []time.Duration
	for _, c := range chunks {
		for i := len(c) - 1; i >= 0; i-- {
			m := reLineTS.FindStringSubmatch(c[i].Text)
			if m == nil {
				continue
			}
			t, err := time.Parse("2006/01/02 15:04:05.999999", m[1])
			if err != nil {
				continue
			}
			seen = append(seen, t.Sub(c[i].ReceivedAt.UTC().Truncate(time.Second)).Round(15*time.Minute))
			break
		}
	}
	if len(seen) == 0 {
		return 0
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
	return seen[len(seen)/2]
}

// Builder gathers the minutes of one window.
type Builder struct {
	from, to time.Time
	minutes  map[time.Time]*Minute
}

// NewBuilder starts the timeline of [from, to).
func NewBuilder(from, to time.Time) *Builder {
	return &Builder{from: from.UTC(), to: to.UTC(), minutes: map[time.Time]*Minute{}}
}

func (b *Builder) minute(at time.Time) *Minute {
	at = at.UTC().Truncate(time.Minute)
	if at.Before(b.from.Truncate(time.Minute)) || !at.Before(b.to) {
		return nil
	}
	m := b.minutes[at]
	if m == nil {
		m = &Minute{At: at}
		b.minutes[at] = m
	}
	return m
}

func (b *Builder) tester(at time.Time) *Tester {
	m := b.minute(at)
	if m == nil {
		return nil
	}
	if m.Tester == nil {
		m.Tester = &Tester{}
	}
	return m.Tester
}

// AddTesterLine reads one line of the tester's log; offset is Offset's.
func (b *Builder) AddTesterLine(text string, offset time.Duration) {
	if mins, ok := downtime.ParseLine(text, "t"); ok {
		for _, m := range mins {
			if m.State == downtime.Flowing || m.State == downtime.Idle {
				continue // an ordinary minute: kept only for what else it holds
			}
			if t := b.tester(m.At); t != nil {
				t.State = string(m.State)
			}
		}
		return
	}
	if stalls, ok := downtime.ParseStallLine(text, "t"); ok {
		for _, s := range stalls {
			if s.N == 0 || s.BoundMS < 1000 {
				continue
			}
			t := b.tester(s.At)
			if t == nil {
				continue
			}
			switch s.BoundMS {
			case 1000:
				t.Stalls1s, t.StallMS = s.N, s.MS
			case 3000:
				t.Stalls3s = s.N
			}
		}
		return
	}
	m := reLineTS.FindStringSubmatch(text)
	if m == nil {
		return
	}
	at, ok := clientTime(m[1], offset)
	if !ok {
		return
	}
	src, msg := m[2], m[3]
	switch {
	case strings.HasPrefix(src, "diag:voice"):
		if v := reVoice.FindStringSubmatch(msg); v != nil {
			if t := b.tester(at); t != nil {
				t.Voice = append(t.Voice, strings.TrimSpace(fmt.Sprintf("path=%s %sgaps60=%s gaps150=%s gap_max=%s", v[1], v[2], v[3], v[4], v[5])))
			}
		}
	case src == "hikkari":
		if p, ok := periodPauses(msg); ok {
			if t := b.tester(at); t != nil {
				t.Pauses = append(t.Pauses, p)
			}
		}
	case src == "connect", src == "route", src == "subscription":
		b.testerEvent(at, src+" "+msg)
	case src == "lifecycle" && (strings.HasPrefix(msg, "begin ") || strings.HasPrefix(msg, "hot ")):
		b.testerEvent(at, src+" "+short(msg, 160))
	case strings.Contains(msg, "health: "):
		b.testerEvent(at, "health "+short(msg[strings.Index(msg, "health: ")+8:], 120))
	}
}

func (b *Builder) testerEvent(at time.Time, e string) {
	if t := b.tester(at); t != nil {
		t.Events = append(t.Events, e)
	}
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// periodPauses reads the v1.06 pauses of a client's hikkari period line.
func periodPauses(msg string) (string, bool) {
	m := rePeriod.FindStringSubmatch(msg)
	if m == nil {
		return "", false
	}
	var p struct {
		Pauses *struct {
			Out map[string]struct {
				Packets int64 `json:"packets"`
				Over60  int64 `json:"over60"`
				Over150 int64 `json:"over150"`
				Stream  int64 `json:"stream"`
			} `json:"out"`
		} `json:"pauses"`
	}
	if err := json.Unmarshal([]byte(m[2]), &p); err != nil || p.Pauses == nil {
		return "", false
	}
	parts := []string{m[1]}
	for _, lane := range []string{"voice", "video"} {
		g, ok := p.Pauses.Out[lane]
		if !ok || g.Packets == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s packets=%d over60=%d over150=%d stream=%d", lane, g.Packets, g.Over60, g.Over150, g.Stream))
	}
	if len(parts) == 1 {
		return "", false
	}
	return strings.Join(parts, " "), true
}

// Our is one thing our side did, at a time.
type Our struct {
	At   time.Time
	What string
}

// AddOurs puts our events into their minutes.
func (b *Builder) AddOurs(ours []Our) {
	for _, o := range ours {
		if m := b.minute(o.At); m != nil {
			m.Ours = append(m.Ours, o.What)
		}
	}
}

// Minutes returns the minutes with something in them, oldest first, each
// stalled minute with our events near it.
func (b *Builder) Minutes(ours []Our) []Minute {
	out := make([]Minute, 0, len(b.minutes))
	for _, m := range b.minutes {
		if m.Tester != nil && m.Tester.Stalls1s > 0 {
			for _, o := range ours {
				d := o.At.Sub(m.At)
				if d >= -NearBy && d < NearBy+time.Minute && o.At.Truncate(time.Minute) != m.At {
					m.Near = append(m.Near, o.At.UTC().Format("15:04:05")+" "+o.What)
				}
			}
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// eventKinds are the entry's events that touch people's flows; the minute's
// "receivers" heartbeat and the unit's notes are left out.
var eventKinds = map[string]bool{
	"entry_generation_left": true, "entry_handoff": true, "entry_drains": true,
	"exit_epoch": true, "exit_drains": true, "receiver_moved": true,
	"route": true, "wires_red": true, "wires_held": true,
}

// OursFromTrunk turns the cascade watch's event and call rows into our
// events: the entry's events by kind, members that died counted per minute,
// and the people's trunk call's minutes with a pause over 150 ms (target
// copies1, the people's entry; the stand's "pipe-" targets are left out).
func OursFromTrunk(entry string, ts time.Time, layer, target string, data map[string]any, deaths map[time.Time]int) (Our, bool) {
	switch layer {
	case "event":
		kind, _ := data["kind"].(string)
		if kind == "member_down" {
			deaths[ts.UTC().Truncate(time.Minute)]++
			return Our{}, false
		}
		if !eventKinds[kind] {
			return Our{}, false
		}
		if path, _ := data["path"].(string); path == "pipe" {
			return Our{}, false // the stand beside the people's entry
		}
		return Our{At: ts, What: entry + " " + kind + describe(data)}, true
	case "call":
		if target != "copies1" {
			return Our{}, false
		}
		g150, _ := data["gaps150"].(float64)
		if g150 <= 0 {
			return Our{}, false
		}
		gmax, _ := data["gap_max_ms"].(float64)
		return Our{At: ts, What: fmt.Sprintf("%s trunk call red: gaps150=%d gap_max=%d ms", entry, int(g150), int(gmax))}, true
	}
	return Our{}, false
}

// DeathsAsOurs turns the per-minute count of dead pool members into events.
func DeathsAsOurs(entry string, deaths map[time.Time]int) []Our {
	var out []Our
	for at, n := range deaths {
		out = append(out, Our{At: at, What: entry + " member_down x" + strconv.Itoa(n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// describe writes an event's fields but its kind, sorted, as k=v.
func describe(data map[string]any) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		if k != "kind" && k != "detail" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, " %s=%v", k, data[k])
	}
	return sb.String()
}
