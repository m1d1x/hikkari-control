package entry

// pulse_api.go is the relays' way in (roadmap 42 П9.3).
//
// Server-to-server behind a bearer, like the prober's ingest beside it — but a
// token of its own, not the prober's. The two are different populations: the
// prober is one vantage we run, the relays are every rented box in the pool,
// and a box is the thing most likely to be taken. Sharing one token would mean
// one seized machine could also poison the probe stream it has no business
// touching. For the same reason the relays do not share one either: each box
// carries [PulseToken] of its own, derived from EDGE_PULSE_TOKEN, and a pulse
// is taken only for a live relay of the box whose token it bears.
//
// An unset token switches the surface off with 503 rather than accepting
// anonymous pulses: a hub that took unsigned counters from anywhere would let
// a stranger write any shape of load into the console.

import (
	"encoding/json"
	"errors"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"net/http"
	"strings"
)

// maxPulseBody bounds one pulse: a dozen numbers plus a pair of totals per
// access key on the relay. Room for some thousands of keys; anything larger
// is not a pulse.
const maxPulseBody = 1 << 20

// pulseBody is the wire shape, shared by contract with edge/pulse.go. The edge
// is a module of its own and cannot import this type, so the contract is the
// JSON and the two comments that describe it.
type pulseBody struct {
	Relay string `json:"relay"`
	// Totals since the edge process started, never deltas: a lost pulse then
	// costs nothing, and a restart is visible as a counter going down.
	UptimeS   int64   `json:"uptimeS"`
	BytesUp   int64   `json:"bytesUp"`
	BytesDown int64   `json:"bytesDown"`
	Conns     int64   `json:"conns"`
	CPUPct    float64 `json:"cpuPct"`
	MemMB     int64   `json:"memMb"`
	Version   string  `json:"version"`
	// Boot and Keys: per-access-key totals of this edge run (pulse.go).
	Boot string                  `json:"boot,omitempty"`
	Keys map[string]pulseKeyBody `json:"keys,omitempty"`
}

type pulseKeyBody struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// pulseAnswer is the reply to a pulse: every access key the relay must let
// through, the whole list each time, so a relay that missed answers catches
// up on the next one. No answer body (204) leaves the relay's list as it is.
type pulseAnswer struct {
	Keys []string `json:"keys"`
}

// RegisterPulseRoutes mounts the relay pulse on the public mux, behind a
// bearer per box. token is EDGE_PULSE_TOKEN, the hub's secret the box tokens
// derive from; empty switches the surface off.
func RegisterPulseRoutes(mux *http.ServeMux, svc *Service, token string) {
	mux.HandleFunc("POST /api/v1/internal/entry-pulse", func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pulse ingest is not configured"})
			return
		}
		// Which box's token this must be depends on the relay the body
		// names, so the body is read first — bounded, and nothing in it is
		// believed until the token matches.
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

		var body pulseBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPulseBody)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not a pulse"})
			return
		}

		err := svc.RecordPulse(r.Context(), token, bearer, PulseReport{
			Relay:     body.Relay,
			UptimeS:   body.UptimeS,
			BytesUp:   body.BytesUp,
			BytesDown: body.BytesDown,
			Conns:     body.Conns,
			CPUPct:    body.CPUPct,
			MemMB:     body.MemMB,
			Version:   body.Version,
			Boot:      body.Boot,
			Keys:      keyUsage(body.Keys),
		})
		switch {
		case errors.Is(err, ErrPulseRefused):
			// Said plainly rather than swallowed: a box whose pulse is not
			// taken is a deployment that went wrong, and the box's own log is
			// where that has to become visible. One answer for every reason,
			// so the refusal does not tell a box which relay names exist.
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		case err != nil:
			applog.Error(r.Context(), "entry", "relay pulse not recorded", applog.F("relay", body.Relay), applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		default:
			keys, ok, err := svc.PulseKeys(r.Context(), body.Relay)
			switch {
			case err != nil:
				// The pulse is filed; only the key list failed. The relay
				// keeps the list it has until the next pulse.
				applog.Error(r.Context(), "entry", "relay keys not read", applog.F("relay", body.Relay), applog.F("err", err.Error()))
				w.WriteHeader(http.StatusNoContent)
			case ok:
				writeJSON(w, http.StatusOK, pulseAnswer{Keys: keys})
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}
	})
}

func keyUsage(keys map[string]pulseKeyBody) map[string]KeyUsage {
	if len(keys) == 0 {
		return nil
	}
	out := make(map[string]KeyUsage, len(keys))
	for k, v := range keys {
		out[k] = KeyUsage{Up: v.Up, Down: v.Down}
	}
	return out
}
