package probesink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
)

// entryProbeStore is the narrow slice of *analytics.Store this handler needs —
// declared on the consumer side (Go house rule) so the handler can be tested
// against a fake without a live ClickHouse.
type entryProbeStore interface {
	InsertEntryProbes(ctx context.Context, records []analytics.EntryProbeRecord) error
}

// entryHub is what the ingest asks the entry pool: which observation point a
// token belongs to, and which of the named relays have already died. Declared
// on the consumer side; *entry.Service satisfies it.
type entryHub interface {
	VantageByToken(ctx context.Context, token string) (string, error)
	DeadRelays(ctx context.Context, names []string) (map[string]bool, error)
}

// incomingEntryProbe matches the JSON shape produced by cmd/entry-prober.
// Field tags use camelCase to match the Go struct on the prober side.
//
// There is no source region: the row's observation point is the slot its
// token belongs to (entry.Service.VantageByToken). A field naming it made
// "which vantage saw this" the sender's word (audit 42-autopilot К1); an older
// prober still sending it is ignored.
type incomingEntryProbe struct {
	TS           time.Time `json:"ts"`
	RelayName    string    `json:"relayName"`
	RawTCP       uint8     `json:"rawTcp"`
	RealityOK    uint8     `json:"realityOk"`
	RealityRTTMs float32   `json:"realityRttMs"`
	RealityHTTP  uint16    `json:"realityHttp"`
	K1BareTLS    uint8     `json:"k1BareTls"`
	K2Control    uint8     `json:"k2Control"`
	Cover        string    `json:"cover"`
	Verdict      string    `json:"verdict"`
	Detail       string    `json:"detail"`
	// The voice door of a stream relay that also carries calls (П9.7): sent
	// and returned, not folded into the verdict. Absent from an older
	// prober's payload, which reads as a door nobody measured — zero of zero.
	XUDPGot  uint16 `json:"xudpGot"`
	XUDPSent uint16 `json:"xudpSent"`
}

type entryIngestRequest struct {
	Probes []incomingEntryProbe `json:"probes"`
}

// IngestEntry returns the POST /api/v1/internal/entry-probes handler. It
// accepts Reality-relay liveness samples from cmd/entry-prober, each behind
// its observation point's own token (issued by the hub per slot, migration
// 074): 401 for a token no active slot holds, and every row is written under
// that slot. A hub with no pool wired answers 503.
//
// A row's time is the prober's word too, and the detector reads the newest
// row per relay as its verdict: one dated in the future would stay "the
// latest" for as long as the dataset keeps it. So a batch with a row outside
// [now-maxRowAge, now+maxClockSkew] is refused whole (400).
//
// A sample about a relay that is already dead when it arrives is dropped. A
// probe round takes the watch list at its start and reports at its end, so a
// machine handed back in between is probed after its instance is gone — and a
// deleted machine reads exactly like a banned one: nothing answers from
// anywhere. Kept, that row was a ban transition the censor never caused, and
// the detection screen counted every manual hand-back as one. A dead relay is
// out of the pool, so no verdict about it says anything about the censor.
func IngestEntry(store entryProbeStore, hub entryHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hub == nil || store == nil {
			http.Error(w, "entry probes ingest disabled", http.StatusServiceUnavailable)
			return
		}
		token, ok := bearer(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		slot, err := hub.VantageByToken(r.Context(), token)
		if err != nil {
			// Unknown, retired and a lookup that failed alike: nothing is
			// written for a sender the hub could not name.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body := http.MaxBytesReader(w, r.Body, maxRequestSize)
		defer body.Close()

		var req entryIngestRequest
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

		records, err := normalizeEntry(slot, req.Probes, time.Now().UTC())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		records, err = dropDead(r.Context(), hub, records)
		if err != nil {
			http.Error(w, "pool error", http.StatusInternalServerError)
			return
		}
		if len(records) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if err := store.InsertEntryProbes(r.Context(), records); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// validEntryVerdict mirrors analytics' Enum8 set; the store coerces anything
// else to "unknown", but rejecting here gives the prober a clear 400 on a
// typo instead of silently logging garbage.
func validEntryVerdict(v string) bool {
	switch v {
	case "", "clean", "ip_banned", "fingerprint", "reality_wins", "network_down", "unknown", "domain_blocked":
		return true
	default:
		return false
	}
}

// normalizeEntry validates each row and converts to EntryProbeRecord, every
// one under slot. Rejects the whole batch on any malformed row so the prober
// sees a clear 400 and we don't silently drop data.
func normalizeEntry(slot string, in []incomingEntryProbe, now time.Time) ([]analytics.EntryProbeRecord, error) {
	out := make([]analytics.EntryProbeRecord, 0, len(in))
	for _, p := range in {
		if p.RelayName == "" {
			return nil, errors.New("relay name required")
		}
		if !validLabel(p.RelayName) {
			return nil, errors.New("invalid relay name label")
		}
		if !validEntryVerdict(p.Verdict) {
			return nil, errors.New("invalid verdict")
		}
		ts, err := rowTime(p.TS, now)
		if err != nil {
			return nil, err
		}
		out = append(out, analytics.EntryProbeRecord{
			Timestamp:    ts,
			SourceRegion: slot,
			RelayName:    p.RelayName,
			RawTCP:       p.RawTCP,
			RealityOK:    p.RealityOK,
			RealityRTTMs: p.RealityRTTMs,
			RealityHTTP:  p.RealityHTTP,
			K1BareTLS:    p.K1BareTLS,
			K2Control:    p.K2Control,
			Cover:        p.Cover,
			Verdict:      p.Verdict,
			Detail:       p.Detail,
			XUDPGot:      p.XUDPGot,
			XUDPSent:     p.XUDPSent,
		})
	}
	return out, nil
}

// dropDead removes the samples about relays the pool already holds dead (see
// IngestEntry). A name the pool does not know at all is kept: whatever it is,
// it was not handed back.
func dropDead(ctx context.Context, pool entryHub, in []analytics.EntryProbeRecord) ([]analytics.EntryProbeRecord, error) {
	names := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, r := range in {
		if !seen[r.RelayName] {
			seen[r.RelayName] = true
			names = append(names, r.RelayName)
		}
	}
	dead, err := pool.DeadRelays(ctx, names)
	if err != nil {
		return nil, err
	}
	if len(dead) == 0 {
		return in, nil
	}
	out := in[:0:0]
	for _, r := range in {
		if !dead[r.RelayName] {
			out = append(out, r)
		}
	}
	return out, nil
}
