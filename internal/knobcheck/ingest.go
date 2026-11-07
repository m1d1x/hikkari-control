package knobcheck

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

// Ingester checks tester rounds as they reach the relay dataset; findings also
// go to the client errors (telemetry_errors), where the errors list groups them
// with everything else that breaks.
type Ingester struct {
	store *Store
	// errs is the client errors table; nil keeps findings in knob_checks only.
	errs telemetry.Sink
}

// NewIngester checks into store; errs may be nil (no ClickHouse).
func NewIngester(store *Store, errs telemetry.Sink) *Ingester {
	return &Ingester{store: store, errs: errs}
}

// ComponentKnob is the errors' component of every finding.
const ComponentKnob = "knob"

// Run checks every interval until ctx ends.
func (in *Ingester) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := in.Once(ctx); err != nil {
			log.Printf("knobcheck: %v", err)
		} else if n > 0 {
			log.Printf("knobcheck: %d rounds", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once checks every round without a check. A round is checked against the
// knobs as they are now, which are the knobs of its time: a change after it
// moves knobs_at past it, and the round is settling, not a mismatch.
func (in *Ingester) Once(ctx context.Context) (int, error) {
	total := 0
	declared := map[string]Declared{}
	for {
		rounds, err := in.store.unchecked(ctx, 500)
		if err != nil || len(rounds) == 0 {
			return total, err
		}
		for _, r := range rounds {
			key := r.Profile + "|" + r.Network
			d, seen := declared[key]
			if !seen {
				if d, err = in.store.declared(ctx, r.Profile, asnOf(r.Network)); err != nil {
					return total, err
				}
				declared[key] = d
			}
			state, findings := Check(r.Profile, r.Network, r.Params, d, r.MeasuredAt)
			// Findings go out before the check is stored: a failed send leaves
			// the round to the next pass. A crash between the two sends a
			// round's findings twice, which counts it double rather than
			// losing it.
			if err := in.report(ctx, r, findings); err != nil {
				return total, err
			}
			if err := in.store.record(ctx, r.RunID, state, d, findings); err != nil {
				return total, err
			}
			total++
		}
	}
}

// report sends a round's findings to the client errors, one row per finding,
// under the tester's install, version and OS.
func (in *Ingester) report(ctx context.Context, r Round, findings []Finding) error {
	if in.errs == nil || len(findings) == 0 {
		return nil
	}
	rows := make([]telemetry.ErrorRow, 0, len(findings))
	for _, f := range findings {
		msg := f.Msg
		if rs := []rune(msg); len(rs) > 300 {
			msg = string(rs[:300])
		}
		rows = append(rows, telemetry.ErrorRow{
			TS: r.MeasuredAt, Install: r.Install,
			App:  telemetry.App{Version: r.Version, Channel: "tester", OS: r.Platform},
			Code: f.Code, Component: ComponentKnob, FP: errorFP(r.Profile, f), Msg: msg, Count: 1,
		})
	}
	if err := in.errs.InsertTelemetry(ctx, nil, rows); err != nil {
		return fmt.Errorf("findings to errors: %w", err)
	}
	return nil
}

// asnOf reads «AS12389»; 0 is no known network.
func asnOf(network string) uint32 {
	n, err := strconv.ParseUint(strings.TrimPrefix(network, "AS"), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}
