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

// maxPulseBody bounds one pulse. A pulse is a dozen numbers; anything larger
// is not a pulse.
const maxPulseBody = 4096

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
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
