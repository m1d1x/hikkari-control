package cascadewatch

import (
	"fmt"
	"time"
)

// The stand over hk-pipe (hikkari-control internal/entry/trunkpipe.go,
// roadmap 4.6): the same trunkentry binary as the entry, a brain over the
// Rust pipe, in a unit of its own beside the entry on the same box. Its
// lines are the entry's (same identifier, same words), so its journal is
// read apart (pipeUnit) and its rows go under targets of their own: its
// call on each wire as pipe-<wire> (pipe-pool, pipe-udp, pipe-h3dgram), its
// two calls of the route as pipe-copies1 and pipe-copies2, beside the
// entry's trunk-<wire> and copies1, copies2 of the same minutes; its
// selector's moves are events with path pipe.

// pipeUnit is the stand's unit.
const pipeUnit = "nekko-trunk-pipe"

// PathPipe is the path of the stand's events.
const PathPipe = "pipe"

// classifyPipe turns a line of the stand into its rows; false for a line the
// watch does not keep of the stand (its minute line, its pipe's ticks).
func classifyPipe(l journalLine) ([]Run, bool) {
	l.Msg = unwrapLogJSON(l.Msg)
	if m := reWireCall.FindStringSubmatch(l.Msg); m != nil && m[1] == "nekko-trunk-entry" {
		run, _ := classifyWire(l)
		run.Target = "pipe-" + m[2]
		return []Run{run}, true
	}
	if m := reCall.FindStringSubmatch(l.Msg); m != nil {
		data := callFields(m[1])
		return []Run{{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerCall, Target: fmt.Sprintf("pipe-copies%d", int(num(data, "copies"))),
			OK: boolp(callOK(data)), Data: data}}, true
	}
	if moved, ok := classifyRoute(l); ok {
		for i := range moved {
			moved[i].Data["path"] = PathPipe
		}
		return moved, true
	}
	return nil, false
}

// pipeJournalCmd reads the stand's journal since the entry's cursor: both are
// read in the same minute, so one cursor serves them.
func pipeJournalCmd(since int64) string {
	return fmt.Sprintf("journalctl -u %s -o short-unix --no-pager --since @%d", pipeUnit, since)
}
