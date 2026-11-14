package main

// The brain's minute line (brain_linux.go): what the pipe's ticks say, in
// the Go entry's words where they mean the same (main.go statsLine), and
// the CPU of both processes, so the cost of a packet reads off the line:
// (cpu_brain_ms + cpu_pipe_ms) / pkts over the same minute (hikkari-client
// plans/research/2026-11-11-cascade-synthetic-users.md measured the Go
// entry's from /proc snapshots by hand).

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// clockTicks is Linux's USER_HZ, the unit of utime and stime in
// /proc/<pid>/stat on every architecture the boxes run.
const clockTicks = 100

// cpuOf is the process's user plus system time.
func cpuOf(pid int) (time.Duration, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	// The command name is in parentheses and may hold spaces: the fields
	// count from after its closing one (proc(5): utime is field 14).
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("/proc/%d/stat: %q", pid, s)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 13 {
		return 0, fmt.Errorf("/proc/%d/stat: %d fields", pid, len(f))
	}
	ut, e1 := strconv.ParseUint(f[11], 10, 64)
	st, e2 := strconv.ParseUint(f[12], 10, 64)
	if e1 != nil || e2 != nil {
		return 0, fmt.Errorf("/proc/%d/stat: utime %q stime %q", pid, f[11], f[12])
	}
	return time.Duration(ut+st) * time.Second / clockTicks, nil
}

// minuteMark is what one minute line counts from.
type minuteMark struct {
	pkts       uint64
	brain, cpu time.Duration
}

func (b *brain) mark(t pipectl.Tick) minuteMark {
	m := minuteMark{pkts: t.DgramsOut + t.DgramsIn}
	m.brain, _ = cpuOf(os.Getpid())
	m.cpu, _ = cpuOf(b.pipe.Pid())
	return m
}

// logMinutes writes the minute line until ctx ends.
func (b *brain) logMinutes(ctx context.Context) {
	tk := time.NewTicker(time.Minute)
	defer tk.Stop()
	t, _ := b.pipe.Tick()
	last := b.mark(t)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		t, _ := b.pipe.Tick()
		now := b.mark(t)
		log.Print("nekko-trunk-entry: " + pipeLine(t, &b.deaths, now, last))
		// The Go entry's minute line too, in its keys: the hub's entry
		// layer, its alerts and summary read it (cascadewatch journal.go).
		if st, err := b.pipe.Stats(); err == nil {
			log.Print("nekko-trunk-entry: " + pipeStatsLine(t, st, &b.deaths))
		} else {
			log.Printf("nekko-trunk-entry: stats: %v", err)
		}
		last = now
	}
}

// pipeLine is the minute line over the pipe: counters since its start as
// the Go entry's, the minute's packets and CPU, then each wire
// (<id>:<up>/<members>:<rtt ms>:<frames up>/<frames down>).
func pipeLine(t pipectl.Tick, d *memberDeaths, now, last minuteMark) string {
	members, up := 0, 0
	wires := make([]string, 0, len(t.Wires))
	for _, w := range t.Wires {
		if w.ID == 0 {
			members, up = w.Members, w.Up
		}
		wires = append(wires, fmt.Sprintf("%d:%d/%d:%.1f:%d/%d", w.ID, w.Up, w.Members,
			float64(w.RTT.Microseconds())/1000, w.UpPkts, w.DownPkts))
	}
	pkts := now.pkts - last.pkts
	brain, cpu := now.brain-last.brain, now.cpu-last.cpu
	perPkt := "-"
	if pkts > 0 {
		perPkt = strconv.FormatFloat(float64((brain+cpu).Microseconds())/float64(pkts), 'f', 1, 64)
	}
	return fmt.Sprintf("pipe members=%d up=%d joined=%d stalled=%d quiet=%d broken=%d streams=%d flows=%d dgrams=%d/%d copies=%d flows_idle=%d pauses=%d late_max_ms=%d retired=%d coming=%v pkts=%d cpu_brain_ms=%d cpu_pipe_ms=%d us_per_pkt=%s wires=%s",
		members, up, d.joined.Load(), d.silent.Load(), d.quiet.Load(), d.gone.Load(), t.Streams, t.Flows,
		t.DgramsOut, t.DgramsIn, t.DgramsCopy, t.FlowsIdle, t.Pauses, t.LateMax.Milliseconds(), t.Retired, t.Coming,
		pkts, brain.Milliseconds(), cpu.Milliseconds(), perPkt, strings.Join(wires, ","))
}

// pipeStatsLine is the Go entry's minute line (main.go statsLine) over the
// pipe: members up of the pool, the members' events since the start (a
// member silent or past Quiet is stalled, one whose socket failed broken),
// and the pipe's counters (pipectl Stats, the tick).
func pipeStatsLine(t pipectl.Tick, st pipectl.Stats, d *memberDeaths) string {
	members := 0
	for _, w := range t.Wires {
		if w.ID == trunk.PoolWire {
			members = w.Up
		}
	}
	ms := func(v time.Duration) string {
		if v < 0 {
			return "-"
		}
		return strconv.FormatFloat(float64(v.Microseconds())/1000, 'f', 1, 64)
	}
	return fmt.Sprintf("members=%d joined=%d stalled=%d broken=%d resent=%d dead_ports=%d port_kills=%d streams=%d/%d reset=%d up=%d down=%d flows=%d dgrams=%d/%d copies=%d pauses=%d paused_ms=%d rtt_p50=%s rtt_p90=%s rtt_max=%s",
		members, d.joined.Load(), d.silent.Load()+d.quiet.Load(), d.gone.Load(), st.Resent, st.DeadPorts, st.PortKills,
		t.Streams, st.Opened, st.Reset, st.Up, st.Down, t.Flows, t.DgramsOut, t.DgramsIn, t.DgramsCopy,
		t.Pauses, st.Paused.Milliseconds(), ms(st.RTTP50), ms(st.RTTP90), ms(st.RTTMax))
}
