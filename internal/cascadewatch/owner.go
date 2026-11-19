package cascadewatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// Notifier tells the owner (internal/notify): the hub knows of a breakage
// before a user says it (roadmap, decision 18.11). 18.11 the people's trunk
// call was red from 16:27 UTC and the owner heard of it from a tester at
// 17:15: the alerts went to the errors table, which nobody reads live.
type Notifier interface {
	Notify(ctx context.Context, text string) error
}

const (
	// ownerRedMinutes red minutes in a row of the people's call tell the
	// owner: the spike's threshold is one red minute a day, three in a row
	// are a breakage and not a pause.
	ownerRedMinutes = 3
	// ownerFresh: only a minute this recent tells; a journal read back
	// after a hub restart replays its old minutes without a word.
	ownerFresh = 5 * time.Minute
	// ownerWireLoss: a wire of the people's entry losing more of its call's
	// datagrams than this ownerWireMinutes minutes in a row tells the owner
	// (the owner, 18.11: «если потери пакетов больше 10% на каком-то пути»).
	// A clean wire loses 0–0.2%; 18.11 udp and h3dgram lost 13–23% in each
	// wave of the entry's network, the pool up to 5%.
	ownerWireLoss    = 0.10
	ownerWireMinutes = 2
)

// lossStreak is one wire of an entry as the owner hears of it: the minutes
// in a row past ownerWireLoss, the worst of them, and whether told.
type lossStreak struct {
	since time.Time
	n     int
	worst float64
	told  bool
	last  time.Time
}

// callStreak is the people's call of one entry as the owner hears of it:
// the red minutes in a row and whether the owner was told of them.
type callStreak struct {
	since time.Time // the first red minute of the streak
	n     int
	told  bool
	last  time.Time // the newest minute heard: a minute read again is skipped
}

// SetOwner tells n of the people's trunk going wrong; nil tells nobody.
func (w *Watch) SetOwner(n Notifier) { w.owner = n }

// hearPeoplesCall takes a minute of the people's trunk call (copies1 of an
// entry's own journal, not the stand's pipe-*): ownerRedMinutes red minutes
// in a row tell the owner once, the first clean one after tells it is over.
func (w *Watch) hearPeoplesCall(ctx context.Context, p entry.TrunkPair, run Run) {
	if run.Target != "copies1" || run.OK == nil {
		return
	}
	w.mu.Lock()
	s := w.calls[p.Entry]
	if s == nil {
		s = &callStreak{}
		w.calls[p.Entry] = s
	}
	if !run.TS.After(s.last) {
		w.mu.Unlock()
		return
	}
	s.last = run.TS
	var text string
	if !*run.OK {
		if s.n == 0 {
			s.since = run.TS
		}
		s.n++
		if s.n >= ownerRedMinutes && !s.told && w.fresh(run.TS) {
			s.told = true
			sent, got := int(num(run.Data, "sent")), int(num(run.Data, "got"))
			text = fmt.Sprintf("🔴 Звонок магистрали людей %s → %s красный %d мин подряд, с %s UTC. Последняя минута: дошло %d из %d, худшая пауза %d мс.",
				p.Entry, p.Exit, s.n, s.since.Format("15:04"), got, sent, int(num(run.Data, "gap_max_ms")))
		}
	} else {
		if s.told {
			text = fmt.Sprintf("🟢 Звонок магистрали людей %s → %s снова чистый с %s UTC, был красным %d мин (с %s UTC).",
				p.Entry, p.Exit, run.TS.Format("15:04"), s.n, s.since.Format("15:04"))
		}
		*s = callStreak{last: run.TS}
	}
	w.mu.Unlock()
	if text != "" {
		w.tellOwner(ctx, p, text)
	}
}

// hearWire takes a minute of the people's entry's call on one wire
// (trunk-udp, trunk-h3dgram, trunk-pool: the entry's own journal, not the
// stand's pipe-*): ownerWireMinutes minutes in a row losing more than
// ownerWireLoss tell the owner once, the first minute under it after tells
// it is over.
func (w *Watch) hearWire(ctx context.Context, p entry.TrunkPair, run Run) {
	wire, ok := strings.CutPrefix(run.Target, "trunk-")
	sent := num(run.Data, "sent")
	if !ok || run.Layer != LayerCall || sent <= 0 {
		return
	}
	loss := num(run.Data, "lost") / sent
	key := p.Entry + "|" + run.Target
	w.mu.Lock()
	s := w.wires[key]
	if s == nil {
		s = &lossStreak{}
		w.wires[key] = s
	}
	if !run.TS.After(s.last) {
		w.mu.Unlock()
		return
	}
	s.last = run.TS
	var text string
	if loss > ownerWireLoss {
		if s.n == 0 {
			s.since = run.TS
		}
		s.n++
		s.worst = max(s.worst, loss)
		if s.n >= ownerWireMinutes && !s.told && w.fresh(run.TS) {
			s.told = true
			text = fmt.Sprintf("🔴 Провод %s входа людей %s → %s теряет %s датаграмм звонка, %d мин подряд с %s UTC.",
				wire, p.Entry, p.Exit, percent(loss), s.n, s.since.Format("15:04"))
		}
	} else {
		if s.told {
			text = fmt.Sprintf("🟢 Провод %s входа людей %s → %s снова теряет %s с %s UTC; терял больше %s %d мин (с %s UTC), худшая минута %s.",
				wire, p.Entry, p.Exit, percent(loss), run.TS.Format("15:04"), percent(ownerWireLoss), s.n, s.since.Format("15:04"), percent(s.worst))
		}
		*s = lossStreak{last: run.TS}
	}
	w.mu.Unlock()
	if text != "" {
		w.tellOwner(ctx, p, text)
	}
}

// percent is a share as the owner reads it: whole percent, one decimal
// under 1.
func percent(f float64) string {
	if f < 0.01 {
		return strings.Replace(fmt.Sprintf("%.1f%%", f*100), ".", ",", 1)
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

// hearPath takes a round's verdict: a path from the entry to its exit that
// loses packets tells the owner, at most once per alertEvery.
func (w *Watch) hearPath(ctx context.Context, p entry.TrunkPair, round time.Time, v Verdict) {
	if v.FirstRed != LayerPath || !w.fresh(round) {
		return
	}
	key := "owner.path|" + p.Entry
	now := w.now()
	w.mu.Lock()
	if at, ok := w.alerted[key]; ok && now.Sub(at) < alertEvery {
		w.mu.Unlock()
		return
	}
	w.alerted[key] = now
	w.mu.Unlock()
	w.tellOwner(ctx, p, fmt.Sprintf("🟠 Путь %s → %s теряет пакеты, раунд %s UTC: %s.",
		p.Entry, p.Exit, round.Format("15:04"), strings.Join(v.Red[LayerPath], "; ")))
}

// fresh is whether a minute at t is recent enough to tell of, and the hub
// up long enough to have read its journals back.
func (w *Watch) fresh(t time.Time) bool {
	now := w.now()
	return now.Sub(w.started) >= silentAfter && now.Sub(t) <= ownerFresh
}

func (w *Watch) tellOwner(ctx context.Context, p entry.TrunkPair, text string) {
	if w.owner == nil {
		return
	}
	if err := w.owner.Notify(ctx, text); err != nil {
		applog.Error(ctx, "cascade", "the owner was not told", applog.F("entry", p.Entry), applog.F("err", err.Error()))
	}
}
