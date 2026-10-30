package entry

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// MaxFailReports bounds one request's reports.
const MaxFailReports = 16

// maxDetail bounds the free-text error class a client attaches.
const maxDetail = 128

// observedWindow is how far back a client's own timestamp is believed.
const observedWindow = 7 * 24 * time.Hour

// Fail stages, as stored (migration 037).
const (
	StageTCP       = "tcp"
	StageTLS       = "tls"
	StageReality   = "reality"
	StageWebSocket = "websocket"
	StageTimeout   = "timeout"
)

var knownStages = map[string]bool{
	StageTCP: true, StageTLS: true, StageReality: true, StageWebSocket: true, StageTimeout: true,
}

// FailReport is one relay attempt that did not get through, as a client
// reported it.
type FailReport struct {
	Relay      string
	Stage      string
	Cover      string
	ObservedAt time.Time
	Detail     string
}

type failMeta struct {
	Cover  string `json:"cover,omitempty"`
	Detail string `json:"detail,omitempty"`
	IP     string `json:"ip,omitempty"`
}

// RecordFailures keeps a client's reports as observations — and does nothing
// else with them. A report never kills a relay and never earns a replacement
// (owner, 16.10): death comes from our probes or the operator, so reporting
// cannot be turned into a way to drain the pool. Thresholds wait for data from
// real relays; these rows are that data.
//
// Kept only for relays this account was handed, once per (account, relay,
// stage) per hour; the rest is dropped, not refused — a client cannot act on
// "your report was a duplicate". Returns how many were kept.
func (s *Service) RecordFailures(ctx context.Context, account uuid.UUID, ip string, reports []FailReport) (int, error) {
	if len(reports) > MaxFailReports {
		reports = reports[:MaxFailReports]
	}
	now := s.now()
	kept := 0
	for _, r := range reports {
		if !nameRe.MatchString(r.Relay) || !knownStages[r.Stage] {
			continue
		}
		observed := r.ObservedAt
		if observed.After(now) || observed.Before(now.Add(-observedWindow)) {
			observed = now
		}
		raw, err := json.Marshal(failMeta{Cover: clip(r.Cover, 253), Detail: clip(r.Detail, maxDetail), IP: ip})
		if err != nil {
			return kept, err
		}
		ct, err := cryptobox.Encrypt(raw)
		if err != nil {
			return kept, err
		}
		ok, err := s.repo.insertFailReport(ctx, account, r.Relay, r.Stage, observed, ct)
		if err != nil {
			return kept, err
		}
		if ok {
			kept++
		}
	}
	return kept, nil
}

// clip cuts s to at most n bytes without splitting a rune.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
