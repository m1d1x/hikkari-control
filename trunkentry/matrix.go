package main

// matrix.go: the entry's matrix of silences (hikkari-client plans research
// 2026-11-19-entry-on-ams3-after-path-silence.md). 19.11 12:10:01 every call
// of the entry went silent 5.3 s, both receivers and the stand, and the data
// could not say whose silence it was: the entry's machine, Aeza's uplink,
// the border, or one receiver. The brain pings, besides the trunk, a few
// targets of three kinds (EDGE_TRUNK_MATRIX) and watches its own box in the
// same seconds:
//
//   - rx: our receivers abroad, a datagram every 20 ms as the trunk's call;
//   - ru: a point inside Russia (the prober ru-eurodir), every 20 ms; it
//     flaps, but one answer in the silence is enough to say Russia was heard;
//   - eu: neutral hosts in the EU outside the fleet (a RIPE Atlas anchor, a
//     hoster's speed test), every 100 ms: not ours, so politely.
//
// The box: a ticker every 20 ms says whether the brain's process ran on time
// (a frozen VM stops it, and steal misses a freeze: ams3 19.11), with steal
// and the interface's drops each second.
//
// Each silence of a receiver of 500 ms or more gets a verdict: box (the
// entry's process did not run for half of it), uplink (Russia silent too:
// the entry's machine or Aeza's network), border (Russia heard, nothing
// abroad), receivers (the neutral EU heard, our receivers not: the path to
// our addresses), receiver:<names> (the others heard), or unknown (Russia
// itself was not heard for long before: no word on it). Lines, one a minute
// for each target and the box, and one for each silence:
//
//	nekko-trunk-entry: matrix target=<name> kind=<k> every_ms=N from=… sent=… got=… rtt_p50=… … worst=…
//	nekko-trunk-entry: matrix box from=… late_max_ms=… late_over100=… late_over500=… steal_max_pct=… steal_avg_pct=… rx_drop=… tx_drop=…
//	nekko-trunk-entry: matrix silence at=<unix ms> len_ms=… silent=a,b heard=c unknown=d box_late_ms=… verdict=…

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// matrixSilence is the shortest silence of a receiver that gets a
	// verdict: the trunk's call alarm (cascade.call_gap) is 500 ms too.
	matrixSilence = 500 * time.Millisecond
	// matrixUnknownAfter: a target not answered for this long before a
	// silence says nothing about it.
	matrixUnknownAfter = 10 * time.Second
	// matrixKeep is how long the answers are kept to judge a silence.
	matrixKeep = 2 * time.Minute
)

// matrixTarget is one target of the matrix.
type matrixTarget struct {
	Name  string
	Kind  string // rx, ru, eu
	Addr  netip.Addr
	Every time.Duration
}

// parseMatrix reads EDGE_TRUNK_MATRIX: kind:name=ip/every_ms, commas.
func parseMatrix(raw string) ([]matrixTarget, error) {
	var out []matrixTarget
	seen := map[string]bool{}
	for _, item := range strings.Split(strings.TrimSpace(raw), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kind, rest, ok := strings.Cut(item, ":")
		name, addr, ok2 := strings.Cut(rest, "=")
		ip, every, ok3 := strings.Cut(addr, "/")
		if !ok || !ok2 || !ok3 || name == "" {
			return nil, fmt.Errorf("EDGE_TRUNK_MATRIX: %q is not kind:name=ip/every_ms", item)
		}
		if kind != "rx" && kind != "ru" && kind != "eu" {
			return nil, fmt.Errorf("EDGE_TRUNK_MATRIX: %q: kind %q is none of rx, ru, eu", item, kind)
		}
		a, err := netip.ParseAddr(ip)
		if err != nil || !a.Is4() {
			return nil, fmt.Errorf("EDGE_TRUNK_MATRIX: %q: %q is not an IPv4 address", item, ip)
		}
		ms, err := strconv.Atoi(every)
		if err != nil || ms < 20 || ms > 1000 {
			return nil, fmt.Errorf("EDGE_TRUNK_MATRIX: %q: every %q is not 20–1000 ms", item, every)
		}
		if seen[name] {
			return nil, fmt.Errorf("EDGE_TRUNK_MATRIX: %s twice", name)
		}
		seen[name] = true
		out = append(out, matrixTarget{Name: name, Kind: kind, Addr: a, Every: time.Duration(ms) * time.Millisecond})
	}
	return out, nil
}

// matrixSeries is what one target answered: when each ping went and each
// answer came, kept matrixKeep.
type matrixSeries struct {
	t       matrixTarget
	sent    []time.Time
	answers []time.Time
	rtts    []time.Duration
}

func (s *matrixSeries) trim(now time.Time) {
	cut := now.Add(-matrixKeep)
	i := sort.Search(len(s.sent), func(i int) bool { return !s.sent[i].Before(cut) })
	s.sent = s.sent[i:]
	j := sort.Search(len(s.answers), func(i int) bool { return !s.answers[i].Before(cut) })
	s.answers, s.rtts = s.answers[j:], s.rtts[j:]
}

// heardIn is how many answers came in (from, to).
func (s *matrixSeries) heardIn(from, to time.Time) int {
	n := 0
	for _, a := range s.answers {
		if a.After(from) && a.Before(to) {
			n++
		}
	}
	return n
}

// lastBefore is the latest answer before t, zero for none kept.
func (s *matrixSeries) lastBefore(t time.Time) time.Time {
	var last time.Time
	for _, a := range s.answers {
		if a.Before(t) {
			last = a
		}
	}
	return last
}

// matrixGapLimit is the pause between answers a target's minute counts as
// a word lost (gaps150): 150 ms, or two pings missed at its pace.
func matrixGapLimit(every time.Duration) time.Duration {
	return max(150*time.Millisecond, every*5/2)
}

// window is the target's minute [from, to): as the trunk's call line has it.
func (s *matrixSeries) window(from, to time.Time) string {
	sent := 0
	for _, t := range s.sent {
		if !t.Before(from) && t.Before(to) {
			sent++
		}
	}
	var rtts []time.Duration
	var arr []time.Time
	for i, a := range s.answers {
		if !a.Before(from) && a.Before(to) {
			arr = append(arr, a)
			rtts = append(rtts, s.rtts[i])
		}
	}
	sort.Slice(rtts, func(a, b int) bool { return rtts[a] < rtts[b] })
	q := func(f float64) string {
		if len(rtts) == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f", float64(rtts[min(len(rtts)-1, int(f*float64(len(rtts))))].Microseconds())/1000)
	}
	limit := matrixGapLimit(s.t.Every)
	gaps60, gaps150 := 0, 0
	var gapMax, silent time.Duration
	type gap struct {
		at  time.Time
		len time.Duration
	}
	var worst []gap
	prev := from
	if len(arr) > 0 {
		prev = arr[0]
	}
	for _, a := range append(arr[min(1, len(arr)):], to) {
		d := a.Sub(prev)
		if d > max(60*time.Millisecond, s.t.Every*3/2) {
			gaps60++
			silent += d - s.t.Every
			worst = append(worst, gap{prev, d})
		}
		if d > limit {
			gaps150++
		}
		gapMax = max(gapMax, d)
		prev = a
	}
	if len(arr) == 0 {
		gapMax = to.Sub(from)
	}
	sort.Slice(worst, func(a, b int) bool { return worst[a].len > worst[b].len })
	ws := []string{}
	for _, g := range worst[:min(len(worst), 5)] {
		ws = append(ws, fmt.Sprintf("%d:%d", g.at.UnixMilli(), g.len.Milliseconds()))
	}
	if len(ws) == 0 {
		ws = []string{"-"}
	}
	return fmt.Sprintf("matrix target=%s kind=%s every_ms=%d from=%d sent=%d got=%d rtt_p50=%s rtt_p99=%s rtt_max=%s gaps60=%d gaps150=%d gap_max=%d silent_ms=%d worst=%s",
		s.t.Name, s.t.Kind, s.t.Every.Milliseconds(), from.Unix(), sent, len(arr), q(0.5), q(0.99), q(1), gaps60, gaps150, gapMax.Milliseconds(), silent.Milliseconds(), strings.Join(ws, ","))
}

// silences is the receivers' silences of matrixSilence or more that ended
// by `until`, from `since`: each [start, end) between two answers.
func (s *matrixSeries) silences(since, until time.Time) [][2]time.Time {
	var out [][2]time.Time
	for i := 1; i < len(s.answers); i++ {
		a, b := s.answers[i-1], s.answers[i]
		if !b.After(since) || b.After(until) {
			continue
		}
		if b.Sub(a) >= matrixSilence {
			out = append(out, [2]time.Time{a, b})
		}
	}
	return out
}

// matrixVerdict judges a silence [from, to) of a receiver by what each
// target answered within it and how late the brain's own ticker ran.
type matrixVerdict struct {
	From, To            time.Time
	Silent, Heard, Unkn []string
	BoxLate             time.Duration
	Verdict             string
}

func (v matrixVerdict) line() string {
	list := func(s []string) string {
		if len(s) == 0 {
			return "-"
		}
		return strings.Join(s, ",")
	}
	return fmt.Sprintf("matrix silence at=%d len_ms=%d silent=%s heard=%s unknown=%s box_late_ms=%d verdict=%s",
		v.From.UnixMilli(), v.To.Sub(v.From).Milliseconds(), list(v.Silent), list(v.Heard), list(v.Unkn), v.BoxLate.Milliseconds(), v.Verdict)
}

// judgeSilence: a target is heard in the silence when one of its answers
// came inside it (past one ping of its pace from the start, before the end),
// unknown when it had not answered for matrixUnknownAfter before it.
func judgeSilence(series []*matrixSeries, from, to time.Time, boxLate time.Duration) matrixVerdict {
	v := matrixVerdict{From: from, To: to, BoxLate: boxLate}
	silent := map[string][]string{}
	heard := map[string][]string{}
	unknown := map[string]bool{}
	for _, s := range series {
		last := s.lastBefore(from.Add(time.Millisecond))
		switch {
		case s.heardIn(from.Add(s.t.Every), to) > 0:
			heard[s.t.Kind] = append(heard[s.t.Kind], s.t.Name)
			v.Heard = append(v.Heard, s.t.Name)
		case last.IsZero() || from.Sub(last) > matrixUnknownAfter:
			unknown[s.t.Kind] = true
			v.Unkn = append(v.Unkn, s.t.Name)
		default:
			silent[s.t.Kind] = append(silent[s.t.Kind], s.t.Name)
			v.Silent = append(v.Silent, s.t.Name)
		}
	}
	length := to.Sub(from)
	switch {
	case boxLate*2 >= length:
		v.Verdict = "box"
	case len(heard["rx"]) > 0 && len(silent["rx"]) > 0:
		v.Verdict = "receiver:" + strings.Join(silent["rx"], "+")
	case len(heard["eu"]) > 0:
		v.Verdict = "receivers"
	case len(heard["ru"]) > 0:
		v.Verdict = "border"
	case len(silent["ru"]) > 0:
		v.Verdict = "uplink"
	default:
		v.Verdict = "unknown"
	}
	return v
}

// boxSecond is the box in one second: how late the brain's ticker ran at
// worst, steal of its CPU time, the interface's drops.
type boxSecond struct {
	At           time.Time
	LateMax      time.Duration
	StealPct     float64
	RxDrop, TxDr uint64
}

// boxMinute is the box's minute line from its seconds and the ticker's
// latenesses in it.
func boxMinute(from time.Time, secs []boxSecond, lates []time.Duration) string {
	var lateMax time.Duration
	over100, over500 := 0, 0
	for _, l := range lates {
		lateMax = max(lateMax, l)
		if l > 100*time.Millisecond {
			over100++
		}
		if l > 500*time.Millisecond {
			over500++
		}
	}
	var stealMax, stealSum float64
	var rx, tx uint64
	for _, s := range secs {
		stealMax = max(stealMax, s.StealPct)
		stealSum += s.StealPct
		rx += s.RxDrop
		tx += s.TxDr
	}
	avg := 0.0
	if len(secs) > 0 {
		avg = stealSum / float64(len(secs))
	}
	return fmt.Sprintf("matrix box from=%d late_max_ms=%d late_over100=%d late_over500=%d steal_max_pct=%.1f steal_avg_pct=%.2f rx_drop=%d tx_drop=%d",
		from.Unix(), lateMax.Milliseconds(), over100, over500, stealMax, avg, rx, tx)
}
