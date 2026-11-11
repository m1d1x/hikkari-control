package stand

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

type fakeCascade struct {
	links    []string
	slot     string
	entry    string
	round    time.Time
	res      Result
	finished int
	load     LoadSpec
	loads    int
}

func (f *fakeCascade) CascadeLinks(_ context.Context, entryBox string) ([]string, error) {
	if entryBox != "ru-entry" {
		return nil, errors.New("unknown entry")
	}
	return f.links, nil
}

func (f *fakeCascade) LoadFinished(_ context.Context, slot, entryBox string, spec LoadSpec, queued time.Time, res Result) error {
	f.slot, f.entry, f.round, f.res, f.load = slot, entryBox, queued, res, spec
	f.loads++
	return nil
}

func (f *fakeCascade) CascadeFinished(_ context.Context, slot, entryBox string, round time.Time, res Result) error {
	f.slot, f.entry, f.round, f.res = slot, entryBox, round, res
	f.finished++
	return nil
}

// A cascade task goes out before the stand's own runs, even with the stand
// switched off, serves the watch's links, and its rows and calls reach both
// the dataset and the watch. A point that has one not done gets no second.
func TestDBCascadeTaskRunsTheWatchsLinks(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	store := relayruns.NewStore(pool)
	s := newTestScheduler(pool, fakeSlots{"ru-spb"}, store)
	watch := &fakeCascade{links: []string{"hikkari+v1://hikkari:k@192.0.2.1:8443?sni=x.example#cascade-entry-unl1-2"}}
	s.SetCascade(watch)
	if _, err := s.Tick(ctx); err != nil { // the stand's own run, queued first
		t.Fatal(err)
	}
	standOn(t, pool, false)
	round := time.Now().UTC().Truncate(time.Minute)
	if n, err := s.QueueCascade(ctx, "ru-entry", round, []string{"ru-spb"}); err != nil || n != 1 {
		t.Fatalf("queued %d: %v", n, err)
	}
	if n, err := s.QueueCascade(ctx, "ru-entry", round.Add(10*time.Minute), []string{"ru-spb"}); err != nil || n != 0 {
		t.Fatalf("a second task while the first is not done: %d %v", n, err)
	}
	task, err := s.Claim(ctx, "ru-spb")
	if err != nil || task == nil || task.Knob != CascadeTaskKind || task.Rounds != 1 || len(task.Values) != 1 || task.Values[0] != "50" {
		t.Fatalf("claimed %+v %v, want the cascade task first and handed out with the stand off", task, err)
	}
	links, err := s.FleetLinks(ctx, linksTokenOf(t, task))
	if err != nil || len(links) != 1 || links[0] != watch.links[0] {
		t.Fatalf("links %q %v", links, err)
	}
	ts := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	res := Result{Core: "sing-box-lx test", Rows: []Row{
		{TS: ts, Profile: "cascade-entry-unl1-2", Knob: Baseline, Value: "no-protocol", Round: 1, Dl: 1000, DlEnd: "ok", Burst: 50, BurstTotal: 50},
		{TS: ts, Profile: "cascade-entry-unl1-2", Knob: LoadBurst, Value: "50", Round: 1, Dl: Bytes, DlEnd: "ok", Burst: 50, BurstTotal: 50, After: 3},
	}, Voice: []CascadeVoice{{Profile: "cascade-entry-unl1-2", Sent: 1500, Got: 1498, Gaps60: 2, GapMax: 80}}}
	added, err := s.Finish(ctx, "ru-spb", task.ID, res)
	if err != nil || added != 2 {
		t.Fatalf("added %d: %v", added, err)
	}
	if watch.finished != 1 || watch.slot != "ru-spb" || watch.entry != "ru-entry" || !watch.round.Equal(round) ||
		len(watch.res.Voice) != 1 || watch.res.Rows[0].Vantage != "ru-spb" {
		t.Fatalf("the watch got %+v", watch)
	}
	if n, err := s.QueueCascade(ctx, "ru-entry", round.Add(10*time.Minute), []string{"ru-spb"}); err != nil || n != 1 {
		t.Fatalf("the next round after the first is done: %d %v", n, err)
	}
}

// A stand run of its own carries no calls.
func TestDBStandRunRefusesCalls(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	s := newTestScheduler(pool, fakeSlots{"ru-spb"}, relayruns.NewStore(pool))
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := s.Claim(ctx, "ru-spb")
	if err != nil || task == nil {
		t.Fatal(task, err)
	}
	if _, err := s.Finish(ctx, "ru-spb", task.ID, Result{Voice: []CascadeVoice{{Profile: "x"}}}); !errors.Is(err, ErrNotAResult) {
		t.Fatalf("calls in a stand run: %v", err)
	}
}

// What a call hears: copies counted once, pauses between answers, the
// longest run of losses.
func TestCallStats(t *testing.T) {
	t0 := time.Unix(1000, 0)
	sent := make([]time.Time, 10)
	for i := range sent {
		sent[i] = t0.Add(time.Duration(i) * 20 * time.Millisecond)
	}
	type a struct {
		seq uint32
		at  time.Time
	}
	// 3, 4, 5 lost; 6 comes 200 ms after 2; 2 twice.
	got := []a{{0, sent[0].Add(30 * time.Millisecond)}, {1, sent[1].Add(30 * time.Millisecond)}, {2, sent[2].Add(30 * time.Millisecond)},
		{2, sent[2].Add(31 * time.Millisecond)}, {6, sent[2].Add(230 * time.Millisecond)}, {7, sent[7].Add(30 * time.Millisecond)},
		{8, sent[8].Add(30 * time.Millisecond)}, {9, sent[9].Add(30 * time.Millisecond)}}
	var v CascadeVoice
	callStats(&v, sent, func(yield func(uint32, time.Time) bool) {
		for _, x := range got {
			if !yield(x.seq, x.at) {
				return
			}
		}
	})
	if v.Got != 7 || v.LossRun != 3 || v.Gaps60 != 1 || v.Gaps150 != 1 || v.GapMax != 200 || v.RTTp50 != 30 {
		t.Fatalf("%+v", v)
	}
}
