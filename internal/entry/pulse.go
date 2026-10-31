package entry

// pulse.go takes in what a relay says about itself (roadmap 42 П9.3): the
// relay's half of the console, beside the prober's verdict from outside and
// the clients' reach telemetry from the far end.
//
// Three vantages, three different questions:
//
//   - the prober (П6.6) asks "can this relay be reached from Russia at all",
//   - the client (П9.9) asks "did a person get through, and how long it took",
//   - the pulse asks "what is this machine doing right now" — the only one of
//     the three that can say a relay is up and idle rather than up and busy.
//
// # What a pulse may carry, and what it may never
//
// Counters, and nothing that identifies whoever made them. No addresses, no
// session list, no per-client anything: a relay that kept such a list would
// become exactly the thing the plan's §0.1 says an edge must never be — a
// place where knowing the machine tells you about the people on it. Bytes and
// a connection count say how loaded a box is and nothing about who loaded it.
//
// # Counters are cumulative, and resets are visible
//
// A pulse reports totals since the edge process started, not deltas since the
// last pulse. A delta is lost forever when a pulse is lost; a total survives
// it, because the next pulse still carries the whole. A restart shows up as a
// counter going down, which a query can see and skip — the alternative, the
// hub trusting a delta it cannot check, hides a restart as a quiet gap.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// PulseInterval is how often an edge is expected to report. The console reads
// a relay as silent after a few missed turns; the exact rule is the
// diagnosis's (П9.4), not this file's.
const PulseInterval = 30 * time.Second

// maxPulseVersion bounds the build string an edge may claim.
const maxPulseVersion = 64

// ErrPulseRefused is every reason a pulse is not taken: a token that is not
// this relay's box's, a relay that is dead or unknown. One error, one answer
// on the wire — a box must not learn from the refusal which names exist.
var ErrPulseRefused = errors.New("entry: pulse refused")

// PulseToken is the bearer box carries for its pulse: HMAC-SHA256 under the
// hub's EDGE_PULSE_TOKEN, keyed by the box's name (audit 42 Н1).
//
// One token per machine, not one for the fleet: a seized box holds the token
// of its own relays and nothing else, so it cannot write numbers into another
// machine's rows. Derived rather than stored — the hub's secret and the box's
// name are all it takes to check one, and nothing new is kept. The hub's own
// secret never leaves the hub; rotating it reissues every box's token at the
// next provisioning.
func PulseToken(master, box string) string {
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte("nekko-entry-pulse\x00" + box))
	return hex.EncodeToString(m.Sum(nil))
}

// PulseReport is one relay's self-report, as it arrived.
type PulseReport struct {
	Relay string
	// UptimeS is how long this edge process has been up. It is how a reader
	// tells "the counters reset because the box restarted" from "the counters
	// reset because something is wrong".
	UptimeS int64
	// BytesUp and BytesDown are totals since that start: up is from client to
	// gateway, down the other way.
	BytesUp   int64
	BytesDown int64
	// Conns is how many connections the edge has accepted since its start,
	// again a total.
	Conns int64
	// CPUPct and MemMB are the box's own load, not the process's share of it:
	// one box runs several relays, and what matters is whether the machine is
	// out of room.
	CPUPct float64
	MemMB  int64
	// Version is the edge build on the box, short sha. It is how a rollout
	// (П6.14) is seen to have landed from the relay's own mouth, rather than
	// from the hub's record of what it pushed.
	Version string
	// Boot names this run of the edge process, so per-key totals are filed
	// against the run they count from.
	Boot string
	// Keys is what each access key carried since the edge started, totals
	// keyed by the key itself (RelayKeys says whose it is).
	Keys map[string]KeyUsage
}

// KeyUsage is bytes one access key carried: up from the client, down to it.
type KeyUsage struct {
	Up, Down int64
}

// RelayKeys owns the access keys subscribers dial with (internal/subscription).
// The pool grants relays to accounts; which key an account dials with, and
// what that key has carried, are the subscription's business, not the pool's.
type RelayKeys interface {
	// KeysOf is every key relay must let through, in proto's credential.
	KeysOf(ctx context.Context, relay, proto string) ([]string, error)
	// RecordUsage files per-key totals of one edge run.
	RecordUsage(ctx context.Context, relay, boot string, usage map[string]KeyUsage) error
}

// SetRelayKeys attaches the access keys; nil leaves relays on their shared
// credential alone and their pulse answered with nothing to change.
func (s *Service) SetRelayKeys(k RelayKeys) { s.relayKeys = k }

// PulseKeys is the key list a relay's pulse is answered with. ok is false
// when the hub keeps no access keys — the relay then leaves its own list as
// it is. Called only after [Service.RecordPulse] took the pulse.
func (s *Service) PulseKeys(ctx context.Context, relay string) (keys []string, ok bool, err error) {
	if s.relayKeys == nil {
		return nil, false, nil
	}
	src, err := s.repo.pulseSource(ctx, relay)
	if err != nil {
		return nil, false, err
	}
	keys, err = s.relayKeys.KeysOf(ctx, relay, src.proto)
	return keys, err == nil, err
}

// PulseRecord is one row of the dataset, as the sink takes it.
type PulseRecord struct {
	TS        time.Time
	Relay     string
	Proto     string
	UptimeS   int64
	BytesUp   int64
	BytesDown int64
	Conns     int64
	CPUPct    float64
	MemMB     int64
	Version   string
}

// PulseSink takes pulses into the dataset (ClickHouse entry_relay_stats).
// Declared here on the consumer side; nil leaves the hub without relay stats
// and costs nothing else.
type PulseSink interface {
	InsertEntryRelayStats(ctx context.Context, rows []PulseRecord) error
}

// SetPulseSink wires the dataset. Called once at startup.
func (s *Service) SetPulseSink(sink PulseSink) { s.pulses = sink }

// RecordPulse keeps one relay's self-report, sent with bearer; master is the
// hub's EDGE_PULSE_TOKEN.
//
// The pulse is taken only from the relay's own machine: bearer must be
// [PulseToken] of the box the relay lives on, and the relay must not be dead.
// Anything else — another box's token, the hub's secret itself, a dead or
// unknown name — is [ErrPulseRefused], the same for all of them. Refused
// rather than dropped: this comes from a machine we provisioned and handed a
// token to, so a refusal means the deployment is wrong, and the box's own log
// is where that shows.
//
// The protocol is resolved here rather than believed from the wire: it is a
// property of the box (migration 049), and a relay that misreported its own
// protocol would put its traffic in another protocol's column.
func (s *Service) RecordPulse(ctx context.Context, master, bearer string, p PulseReport) error {
	if master == "" || !nameRe.MatchString(p.Relay) {
		return ErrPulseRefused
	}
	src, err := s.repo.pulseSource(ctx, p.Relay)
	if errors.Is(err, ErrNoSuchRelay) {
		return ErrPulseRefused
	}
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(bearer), []byte(PulseToken(master, src.box))) || src.status == StatusDead {
		return ErrPulseRefused
	}
	proto := src.proto
	if s.relayKeys != nil && len(p.Keys) > 0 {
		if err := s.relayKeys.RecordUsage(ctx, p.Relay, p.Boot, p.Keys); err != nil {
			return err
		}
	}
	if s.pulses == nil {
		return nil
	}
	return s.pulses.InsertEntryRelayStats(ctx, []PulseRecord{{
		TS:        s.now(),
		Relay:     p.Relay,
		Proto:     proto,
		UptimeS:   nonNegative(p.UptimeS),
		BytesUp:   nonNegative(p.BytesUp),
		BytesDown: nonNegative(p.BytesDown),
		Conns:     nonNegative(p.Conns),
		CPUPct:    clampFloat(p.CPUPct, 0, 100),
		MemMB:     nonNegative(p.MemMB),
		Version:   clip(p.Version, maxPulseVersion),
	}})
}

// nonNegative floors a counter at zero: these are totals, and a negative one
// is a bug on the box, not a measurement.
func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// clampFloat holds v inside [lo, hi].
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
