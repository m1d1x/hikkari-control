package main

import (
	"bytes"
	"log"
	"regexp"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// A pool member of the pipe that dies is said in the Go entry's line, the
// hub's member_down event (cascadewatch journal.go reMemberDown): its index,
// how long it lived, stalled or broken; another wire's member is counted,
// not said.
func TestAPoolMembersDeathIsSaidInTheGoEntrysLine(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(log.Writer())
	var d memberDeaths
	d.event(trunk.PoolWire, 3, true, pipectl.WhyHello)
	d.event(trunk.PoolWire, 4, true, pipectl.WhyHello)
	d.up[3] = time.Now().Add(-90 * time.Second)
	d.event(trunk.PoolWire, 3, false, pipectl.WhySilent)
	d.event(trunk.PoolWire, 4, false, pipectl.WhyGone)
	d.event(3, 0, true, pipectl.WhyHello)
	d.event(3, 0, false, pipectl.WhyQuiet)
	re := regexp.MustCompile(`trunk: member (\d+): down after (\S+): (.*)$`)
	var got []string
	for _, l := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if m := re.FindSubmatch(l); m != nil {
			got = append(got, string(m[1])+" "+string(m[2])+" "+string(m[3][:7]))
		}
	}
	want := []string{"3 1m30s stalled", "4 0s broken:"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("lines %q, want %q", got, want)
	}
	if d.joined.Load() != 3 || d.silent.Load() != 1 || d.gone.Load() != 1 || d.quiet.Load() != 1 {
		t.Fatalf("counts joined %d silent %d gone %d quiet %d", d.joined.Load(), d.silent.Load(), d.gone.Load(), d.quiet.Load())
	}
}
