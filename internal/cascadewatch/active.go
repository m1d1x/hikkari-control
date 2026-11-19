package cascadewatch

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// The receiver the people's entry carries everything on (hikkari-client
// plans/2026-11-16-second-receiver.md). The pair's Exit is the receiver of
// the settings, the main one; the brain moves the entry to a spare when the
// main one dies. 19.11 12:10 UTC the entry moved to ams3 and stood there,
// and the call's rows and the owner's alerts said «aezamsk1 → unl1» the
// whole time: they took the pair, not the receiver. The watch follows the
// brain's receiver_moved and its minute's receivers line and names the
// active receiver in the call's rows (data "receiver") and in every alert
// of the people's call.

const (
	// ownerSpareMinutes on a spare receiver tell the owner once: a spare is
	// there for a death, and the people on it are on a worse machine.
	ownerSpareMinutes = 10
	// judgeDeadAfter is the brain's DeadAfter (hikkari-protocol
	// trunk/pipectl/judge.go): a move later than it by more than judgeSlack
	// waited for a standby to be heard, so the standby was silent too.
	judgeDeadAfter = 1500 * time.Millisecond
	judgeSlack     = time.Second
)

var reUnheardFor = regexp.MustCompile(`unheard for ([0-9.]+)(ms|s)\b`)

// receiverStay is the receiver the people of an entry are on: since when,
// the newest event heard, and whether the owner was told of a long stay on
// a spare.
type receiverStay struct {
	box   string
	since time.Time
	last  time.Time
	told  bool
}

// activeOf is the receiver p's entry carries the people on: the pair's
// receiver until the brain said another.
func (w *Watch) activeOf(p entry.TrunkPair) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.active[p.Entry]; s != nil && s.box != "" && s.box != "?" {
		return s.box
	}
	return p.Exit
}

// receiverOf is the active receiver a run of the call is labelled with, the
// pair's receiver for a row without the label.
func receiverOf(p entry.TrunkPair, run Run) string {
	if s, ok := run.Data["receiver"].(string); ok && s != "" {
		return s
	}
	return p.Exit
}

// hearReceiver takes an event about the receivers of p's entry: it moves the
// active receiver, tells the owner of every move, and of the people on a
// spare for ownerSpareMinutes.
func (w *Watch) hearReceiver(ctx context.Context, p entry.TrunkPair, run Run) {
	kind, _ := run.Data["kind"].(string)
	var texts []string
	w.mu.Lock()
	if w.active == nil {
		w.active = map[string]*receiverStay{}
	}
	s := w.active[p.Entry]
	if s == nil {
		s = &receiverStay{}
		w.active[p.Entry] = s
	}
	if run.TS.Before(s.last) {
		w.mu.Unlock()
		return
	}
	s.last = run.TS
	switch kind {
	case "receiver_moved":
		from, _ := run.Data["from"].(string)
		to, _ := run.Data["to"].(string)
		why, _ := run.Data["why"].(string)
		stayed := ""
		if s.box != "" && s.box != p.Exit && s.box == from && !s.since.IsZero() {
			stayed = fmt.Sprintf(" На %s люди были %d мин.", from, int(run.TS.Sub(s.since).Minutes()))
		}
		*s = receiverStay{box: to, since: run.TS, last: run.TS}
		if w.fresh(run.TS) {
			texts = append(texts, fmt.Sprintf("🟡 Вход людей %s перешёл с приёмника %s на %s в %s UTC: %s.%s",
				p.Entry, from, to, run.TS.Format("15:04:05"), moveWhy(why), stayed))
		}
	case "receivers":
		rs, _ := run.Data["receivers"].([]map[string]any)
		active, up := "", 0
		for _, r := range rs {
			if r["role"] == "active" {
				active, _ = r["box"].(string)
				up, _ = r["up"].(int)
			}
		}
		if active == "" || active == "?" {
			break
		}
		if active != s.box {
			*s = receiverStay{box: active, since: run.TS, last: run.TS}
		}
		if active != p.Exit && !s.told && run.TS.Sub(s.since) >= ownerSpareMinutes*time.Minute && w.fresh(run.TS) {
			s.told = true
			texts = append(texts, fmt.Sprintf("🟡 Люди входа %s на запасном приёмнике %s %d мин, с %s UTC (членов пула %d). Сам вход обратно на %s не переходит.",
				p.Entry, active, int(run.TS.Sub(s.since).Minutes()), s.since.Format("15:04"), up, p.Exit))
		}
	}
	w.mu.Unlock()
	for _, t := range texts {
		w.tellOwner(ctx, p, t)
	}
}

// moveWhy is the brain's why of a move as the owner reads it: by hand, or
// how long the active receiver went unheard and whether the spare was
// silent too.
func moveWhy(why string) string {
	if why == "by hand" {
		return "руками"
	}
	what := "активный приёмник не слышен"
	if strings.Contains(why, "pool") {
		what = "пул активного приёмника не слышен"
	}
	m := reUnheardFor.FindStringSubmatch(why)
	if m == nil {
		return strings.TrimPrefix(why, "dead: ")
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	d := time.Duration(v * float64(time.Second))
	if m[2] == "ms" {
		d = time.Duration(v * float64(time.Millisecond))
	}
	text := fmt.Sprintf("%s %s с", what, seconds(d))
	if d > judgeDeadAfter+judgeSlack {
		text += fmt.Sprintf("; запасной тоже молчал около %s с, то есть молчал весь путь со стороны входа, а не один приёмник", seconds(d-judgeDeadAfter))
	}
	return text
}

// seconds is d in seconds with one decimal and a comma.
func seconds(d time.Duration) string {
	return strings.Replace(fmt.Sprintf("%.1f", d.Seconds()), ".", ",", 1)
}
