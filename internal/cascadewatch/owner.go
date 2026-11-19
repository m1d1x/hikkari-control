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
)

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
