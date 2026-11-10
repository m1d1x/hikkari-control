package hikkariobs

import (
	"context"
	"encoding/json"
	"time"
)

// Sink takes rows (analytics.Store).
type Sink interface {
	InsertHikkariObs(ctx context.Context, rows []Row) error
}

// Accounts names the account behind each access key (subscription.Service).
type Accounts interface {
	AccountsOf(ctx context.Context, keys []string) (map[string]string, error)
}

// Recorder files periods from relays and clients.
type Recorder struct {
	Sink     Sink
	Accounts Accounts
	Now      func() time.Time
}

// Subjects that are not an account.
const (
	SubjectProbe = "probe" // the relay's own probe credential (the empty key)
	SubjectNone  = ""      // a key the hub does not know, or the trunk
)

// maxPeriodS bounds the time a period claims to cover: an edge keeps
// undelivered periods to the next pulse, but not for days.
const maxPeriodS = 24 * 3600

// RecordRelayPeriods files a relay's pulse periods ([entry.HikkariObs]). The
// key is replaced by its account and never stored.
func (r *Recorder) RecordRelayPeriods(ctx context.Context, relay string, seconds int64, periods map[string]json.RawMessage) error {
	keys := make([]string, 0, len(periods))
	for group := range periods {
		if _, key := SplitGroup(group); key != "" {
			keys = append(keys, key)
		}
	}
	accounts, err := r.Accounts.AccountsOf(ctx, keys)
	if err != nil {
		return err
	}
	now := r.now()
	var rows []Row
	for group, period := range periods {
		path, key := SplitGroup(group)
		subject := accounts[key]
		if key == "" && path != "trunk" {
			subject = SubjectProbe
		}
		got, err := Rows(Row{TS: now, Side: SideRelay, Relay: relay, Path: clipName(path), Subject: subject, PeriodS: periodS(seconds)}, period)
		if err != nil {
			return err
		}
		rows = append(rows, got...)
	}
	return r.Sink.InsertHikkariObs(ctx, rows)
}

// RecordClientPeriod files one period a client wrote in its log (source
// hikkari), under the install that sent it; the client's row keeps the
// profile it came by in Relay.
func (r *Recorder) RecordClientPeriod(ctx context.Context, ts time.Time, install, profile string, seconds int64, period json.RawMessage) error {
	rows, err := Rows(Row{TS: ts, Side: SideClient, Relay: profile, Subject: install, PeriodS: periodS(seconds)}, period)
	if err != nil {
		return err
	}
	return r.Sink.InsertHikkariObs(ctx, rows)
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func periodS(seconds int64) uint32 {
	return uint32(min(max(seconds, 0), maxPeriodS))
}

func clipName(s string) string {
	if !validName(s) {
		return "other"
	}
	return s
}
