package downdetector

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server is the HTTP API. Handlers never log requests: no IPs, no bodies,
// no tokens.
type Server struct {
	Store  *Store
	Prober *Prober
	DB     *DB
	Auth   *Auth
	K      int
	// OverviewMaxHours caps GET /v1/overview?hours=.
	OverviewMaxHours int
	Now              func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/reports", s.postReport)
	mux.HandleFunc("GET /v1/status", s.getStatus)
	mux.HandleFunc("GET /v1/overview", s.getOverview)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) postReport(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.Auth.Invite(r)
	if !ok {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	now := s.now()
	if !s.Auth.Allow(tok, now) {
		httpError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	rep, err := DecodeReport(r.Body, now)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.Add(rep); err != nil {
		if errors.Is(err, ErrTooManySubmits) {
			httpError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		httpError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServerStatus is one entry of GET /v1/status.
type ServerStatus struct {
	ID                  string   `json:"id"`
	External            External `json:"external"`
	SameASN             Group    `json:"same_asn"`
	SameCountryOtherASN Group    `json:"same_country_other_asn"`
	Abroad              Group    `json:"abroad"`
	VerdictIfDown       Verdict  `json:"verdict_if_down"`
}

func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	tok, ok := s.Auth.Invite(r)
	if !ok {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	now := s.now()
	if !s.Auth.Allow(tok, now) {
		httpError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	q := r.URL.Query()
	asn, err := strconv.ParseUint(q.Get("asn"), 10, 32)
	if q.Get("asn") == "" {
		asn, err = 0, nil
	}
	if err != nil {
		httpError(w, http.StatusBadRequest, "asn")
		return
	}
	country := q.Get("country")
	if country != "" && !reCountry.MatchString(country) {
		httpError(w, http.StatusBadRequest, "country")
		return
	}
	ids := strings.Split(q.Get("ids"), ",")
	if q.Get("ids") == "" || len(ids) > MaxServers {
		httpError(w, http.StatusBadRequest, "ids")
		return
	}
	out := make([]ServerStatus, 0, len(ids))
	for _, id := range ids {
		if !ValidServerID(id) {
			httpError(w, http.StatusBadRequest, "ids")
			return
		}
		same, country2, abroad := s.Store.Groups(id, uint32(asn), country, now)
		ext := s.Prober.External(id)
		out = append(out, ServerStatus{
			ID:                  id,
			External:            ext,
			SameASN:             same,
			SameCountryOtherASN: country2,
			Abroad:              abroad,
			VerdictIfDown:       Decide(ext, same, country2, abroad, s.K),
		})
	}
	writeJSON(w, out)
}

func (s *Server) getOverview(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.Admin(r) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > s.OverviewMaxHours {
			httpError(w, http.StatusBadRequest, "hours")
			return
		}
		hours = n
	}
	since := s.now().Add(-time.Duration(hours) * time.Hour).Unix()
	since -= since % 3600
	rows, err := s.DB.Overview(r.Context(), since)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, rows)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
