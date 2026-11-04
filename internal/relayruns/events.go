package relayruns

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Kinds of a knob event (relay_knob_events, migration 026): what the knob
// controller (internal/stand controller.go) did to a node, or could not do.
const (
	EventSwitch     = "switch"      // the node moved to another value
	EventAllRed     = "all_red"     // the value is cut and no candidate passes
	EventAddressCut = "address_cut" // the address itself is cut: no knob helps
	// EventNetworkRed: a network's testers see the value red while no point
	// of the stand measures there, so nothing can be chosen for it on
	// evidence (migration 027).
	EventNetworkRed = "network_red"
)

// KnobEvent is one line of the controller's journal.
type KnobEvent struct {
	At      time.Time `json:"at"`
	Node    string    `json:"node"`
	Profile string    `json:"profile"`
	Knob    string    `json:"knob"`
	Kind    string    `json:"kind"`
	// ASN is the subscribers' network the line is about; 0 is the node's own
	// value, the one everyone else gets.
	ASN      uint32          `json:"asn,omitempty"`
	From     string          `json:"from,omitempty"`
	To       string          `json:"to,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

// RecordKnobEvent writes ev on q, which may be the transaction that made the
// switch, so a switch and its line land together.
func RecordKnobEvent(ctx context.Context, q db.DBTX, ev KnobEvent) error {
	evidence := ev.Evidence
	if len(evidence) == 0 {
		evidence = json.RawMessage("{}")
	}
	_, err := q.Exec(ctx, `
		INSERT INTO relay_knob_events (node, profile, knob, kind, asn, from_value, to_value, reason, evidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ev.Node, ev.Profile, ev.Knob, ev.Kind, int64(ev.ASN), ev.From, ev.To, ev.Reason, []byte(evidence))
	if err != nil {
		return fmt.Errorf("record knob event: %w", err)
	}
	return nil
}

// KnobEvents reads the journal since a time, newest first; profile and knob
// narrow it when set.
func (s *Store) KnobEvents(ctx context.Context, profile, knob string, since time.Time, limit int) ([]KnobEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT at, node, profile, knob, kind, asn, from_value, to_value, reason, evidence
		FROM relay_knob_events
		WHERE at > $1 AND ($2 = '' OR profile = $2) AND ($3 = '' OR knob = $3)
		ORDER BY at DESC LIMIT $4`, since, profile, knob, limit)
	if err != nil {
		return nil, fmt.Errorf("read knob events: %w", err)
	}
	defer rows.Close()
	out := []KnobEvent{}
	for rows.Next() {
		var ev KnobEvent
		var raw []byte
		var asn int64
		if err := rows.Scan(&ev.At, &ev.Node, &ev.Profile, &ev.Knob, &ev.Kind, &asn, &ev.From, &ev.To, &ev.Reason, &raw); err != nil {
			return nil, err
		}
		ev.ASN = uint32(asn)
		ev.Evidence = raw
		out = append(out, ev)
	}
	return out, rows.Err()
}
