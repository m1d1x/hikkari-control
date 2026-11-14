package cascadewatch

import (
	"regexp"
	"time"
)

// The leg client → entry (hikkari-control trunkentry/clients.go): once a
// minute the entry adds up its clients' TCP sockets, no client named: the
// segments it sent, how many it sent again, the round trips at p50 and p90.
// A row of layer clients, target tcp, at the minute's start.

// LayerClients is that minute (migration 065).
const LayerClients = "clients"

var reClients = regexp.MustCompile(`nekko-trunk-entry: clients tcp (from=\d+ .*)$`)

// classifyClients turns the entry's clients line into a row; false for any
// other line.
func classifyClients(l journalLine) (Run, bool) {
	m := reClients.FindStringSubmatch(l.Msg)
	if m == nil {
		return Run{}, false
	}
	data := wireFields(m[1])
	return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerClients, Target: "tcp", OK: boolp(true), Data: data}, true
}
