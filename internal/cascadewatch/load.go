package cascadewatch

import (
	"context"
	"time"

	"github.com/m1d1x/hikkari-control/internal/stand"
)

// LoadFinished writes a synthetic users task (stand/load.go) as rows of layer
// load, one per stream: target is the stream (user1/voice), the task's spec
// rides on every row, and ok says the stream carried with no pause a call
// hears (over 150 ms), path which way the streams went (entry, direct, raw).
// Round is when the task was queued, so a task's rows read together.
func (w *Watch) LoadFinished(ctx context.Context, slot, entryBox string, spec stand.LoadSpec, queued time.Time, res stand.Result) error {
	p, err := w.pair(ctx, entryBox)
	if err != nil {
		return err
	}
	rt := queued.UTC()
	base := func() map[string]any {
		return map[string]any{"synthetic": true, "core": res.Core, "users": spec.Users, "minutes": spec.Minutes,
			"game_pps": spec.GamePPS, "echo": spec.Echo.String(), "path": spec.Path}
	}
	var runs []Run
	for _, v := range res.Voice {
		d := base()
		d["sent"], d["got"], d["loss_run"] = v.Sent, v.Got, v.LossRun
		d["gaps60"], d["gaps150"], d["gap_max_ms"] = v.Gaps60, v.Gaps150, v.GapMax
		d["rtt_p50_ms"], d["jitter_ms"] = v.RTTp50, v.Jitter
		if len(v.Gaps60PerMinute) > 0 {
			d["gaps60_per_min"] = v.Gaps60PerMinute
		}
		ok := v.Error == "" && v.Got > 0 && v.Gaps150 == 0
		if v.Error != "" {
			d["error"] = clip(v.Error)
		}
		runs = append(runs, Run{TS: w.now().UTC(), Round: &rt, Entry: p.Entry, Exit: p.Exit, Layer: LayerLoad,
			Vantage: slot, Target: v.Profile, OK: boolp(ok), Data: d})
	}
	if len(runs) == 0 {
		d := base()
		why := "задача без потоков"
		if res.Error != "" {
			why = clip(res.Error)
		}
		d["why"] = "не измерено: " + why
		runs = append(runs, Run{TS: w.now().UTC(), Round: &rt, Entry: p.Entry, Exit: p.Exit, Layer: LayerLoad,
			Vantage: slot, Target: "none", OK: boolp(false), Data: d})
	} else if res.Error != "" {
		for i := range runs {
			runs[i].Data["task_error"] = clip(res.Error)
		}
	}
	return w.store.Insert(ctx, runs...)
}
