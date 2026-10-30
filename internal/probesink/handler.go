// Package probesink accepts synthetic-network samples from cmd/sfu-prober
// and cmd/entry-prober and forwards them to ClickHouse. This is
// server-to-server traffic — no user JWT involved. The SFU prober's bearer is
// SFU_PROBE_TOKEN, configured on both ends; an entry prober's is its
// observation point's own, issued by the hub (IngestEntry). The two never
// share one: a stolen SFU prober must not write the verdicts that evict
// relays.
package probesink

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
)

const (
	maxProbesPerBatch = 100
	maxRequestSize    = 64 * 1024 // 64 KB — generous; one row is ~80 bytes

	// A row's time is the sender's, and readers take the newest row as the
	// latest word. One from the future is refused: it would stay "the latest"
	// until the dataset's TTL. One older than a probe round can take on a
	// large pool is refused too — nothing sends rows that late.
	maxClockSkew = 2 * time.Minute
	maxRowAge    = time.Hour
)

// rowTime is a row's timestamp as the hub accepts it: unset reads as now,
// anything outside [now-maxRowAge, now+maxClockSkew] is an error.
func rowTime(ts, now time.Time) (time.Time, error) {
	if ts.IsZero() {
		return now, nil
	}
	if ts.After(now.Add(maxClockSkew)) || ts.Before(now.Add(-maxRowAge)) {
		return time.Time{}, errors.New("row time out of range (clock skew or a stale batch)")
	}
	return ts, nil
}

// incomingProbe matches the JSON shape produced by cmd/sfu-prober.
// Field tags use camelCase to match the Go struct on the prober side.
type incomingProbe struct {
	TS            time.Time `json:"ts"`
	SourceRegion  string    `json:"sourceRegion"`
	TargetRegion  string    `json:"targetRegion"`
	ProbeType     string    `json:"probeType"`
	RTTMs         float32   `json:"rttMs"`
	JitterMs      float32   `json:"jitterMs"`
	PacketLossPct float32   `json:"packetLossPct"`
	Success       uint8     `json:"success"`
	PacketsSent   uint16    `json:"packetsSent"`
	PacketsRecv   uint16    `json:"packetsRecv"`
}

type ingestRequest struct {
	Probes []incomingProbe `json:"probes"`
}

// Ingest returns the POST /api/v1/internal/probes handler. expectedToken is
// the SFU prober's bearer (SFU_PROBE_TOKEN); when empty the endpoint returns
// 503 (feature off).
func Ingest(store *analytics.Store, expectedToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if expectedToken == "" || store == nil {
			http.Error(w, "probes ingest disabled", http.StatusServiceUnavailable)
			return
		}
		// Constant-time bearer compare. Token leaks via timing aren't a
		// realistic attack here (UDP probe data is low-value), but the
		// pattern is cheap and consistent with auth/handlers.go.
		got := r.Header.Get("Authorization")
		if !validBearer(got, expectedToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body := http.MaxBytesReader(w, r.Body, maxRequestSize)
		defer body.Close()

		var req ingestRequest
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if len(req.Probes) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(req.Probes) > maxProbesPerBatch {
			http.Error(w, "batch too large", http.StatusRequestEntityTooLarge)
			return
		}

		records, err := normalize(req.Probes, time.Now().UTC())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := store.InsertProbes(r.Context(), records); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// normalize validates each row and converts to ProbeRecord. Rejects the
// whole batch on any malformed row so the prober sees a clear 400 and we
// don't silently drop data.
func normalize(in []incomingProbe, now time.Time) ([]analytics.ProbeRecord, error) {
	out := make([]analytics.ProbeRecord, 0, len(in))
	for _, p := range in {
		if p.SourceRegion == "" || p.TargetRegion == "" {
			return nil, errors.New("source and target regions required")
		}
		if !validLabel(p.SourceRegion) || !validLabel(p.TargetRegion) {
			return nil, errors.New("invalid region label")
		}
		ts, err := rowTime(p.TS, now)
		if err != nil {
			return nil, err
		}
		p.TS = ts
		probeType := p.ProbeType
		if probeType == "" {
			probeType = "udp"
		}
		out = append(out, analytics.ProbeRecord{
			Timestamp:     p.TS,
			SourceRegion:  p.SourceRegion,
			TargetRegion:  p.TargetRegion,
			ProbeType:     probeType,
			RTTMs:         p.RTTMs,
			JitterMs:      p.JitterMs,
			PacketLossPct: p.PacketLossPct,
			Success:       p.Success,
			PacketsSent:   p.PacketsSent,
			PacketsRecv:   p.PacketsRecv,
		})
	}
	return out, nil
}

// validLabel mirrors admin.validRegion — letters/digits/dash/underscore.
func validLabel(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// bearer is the token of an "Authorization: Bearer <token>" header.
func bearer(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return "", false
	}
	return header[len(prefix):], true
}

func validBearer(header, expected string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	got := header[len(prefix):]
	// Length first, then byte equality. crypto/subtle would be nicer but
	// the package isn't imported widely here and the token is fixed-length
	// in practice (env-set hex/random).
	if len(got) != len(expected) {
		return false
	}
	var diff byte
	for i := 0; i < len(got); i++ {
		diff |= got[i] ^ expected[i]
	}
	return diff == 0
}
