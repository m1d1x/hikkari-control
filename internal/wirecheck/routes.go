package wirecheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// Resolver turns a tester reference (tester-003, 003, an install id or a
// prefix) into the install id.
type Resolver func(ctx context.Context, ref string) (string, error)

// maxReferenceRows bounds one recording: every fingerprint name of a core is
// a few dozen.
const maxReferenceRows = 200

// RegisterOpsRoutes mounts the agent's view: the lines read with their
// findings, and the reference table, which a recording of a new core revision
// writes (hikkari-client diagnostics/wire_record_test.go).
func RegisterOpsRoutes(mux *http.ServeMux, s *Store, resolve Resolver, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/wire", gate("", func(w http.ResponseWriter, r *http.Request) {
		q := Query{Profile: r.URL.Query().Get("profile"), Findings: r.URL.Query().Get("findings") == "1"}
		q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
		span := 24 * time.Hour
		if v := r.URL.Query().Get("last"); v != "" {
			d, err := parseSpan(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "last: 6h, 24h, 7d"})
				return
			}
			span = d
		}
		q.Since = time.Now().Add(-span)
		if who := r.URL.Query().Get("who"); who != "" {
			id, err := resolve(r.Context(), who)
			switch {
			case errors.Is(err, support.ErrAliasNotFound):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "install not found"})
				return
			case errors.Is(err, support.ErrAmbiguousInstall):
				writeJSON(w, http.StatusConflict, map[string]string{"error": "reference matches several installs — be more specific"})
				return
			case err != nil:
				applog.Error(r.Context(), "wirecheck", "resolve failed", applog.F("err", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			q.Install = id
		}
		hellos, err := s.Hellos(r.Context(), q)
		if err != nil {
			applog.Error(r.Context(), "wirecheck", "read failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"hellos": hellos})
	}))
	mux.HandleFunc("GET /api/v1/ops/wire/reference", gate("", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.References(r.Context(), r.URL.Query().Get("core"))
		if err != nil {
			applog.Error(r.Context(), "wirecheck", "references failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"reference": rows})
	}))
	mux.HandleFunc("POST /api/v1/ops/wire/reference", gate("ops.wire.reference", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Core   string        `json:"core"`
			Source string        `json:"source"`
			Rows   []RefHelloRow `json:"rows"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body: {core, source, rows: [{fp, hk, ja4}]}"})
			return
		}
		if len(body.Rows) == 0 || len(body.Rows) > maxReferenceRows {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rows: 1 to 200"})
			return
		}
		rows := make([]RefRow, 0, len(body.Rows))
		for _, row := range body.Rows {
			ref := RefRow{Core: body.Core, FP: row.FP, HK: row.HK, JA4: row.JA4, Source: body.Source}
			if err := ref.Validate(); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": row.FP + ": " + err.Error()})
				return
			}
			rows = append(rows, ref)
		}
		if err := s.PutReference(r.Context(), rows); err != nil {
			applog.Error(r.Context(), "wirecheck", "put reference failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"stored": len(rows)})
	}))
}

// RefHelloRow is one fingerprint of a recording as posted.
type RefHelloRow struct {
	FP  string `json:"fp"`
	HK  string `json:"hk"`
	JA4 string `json:"ja4"`
}

// parseSpan reads 30m, 6h or 7d.
func parseSpan(v string) (time.Duration, error) {
	if n := len(v); n > 1 && v[n-1] == 'd' {
		days, err := strconv.Atoi(v[:n-1])
		if err != nil || days <= 0 || days > 90 {
			return 0, errors.New("bad span")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > 90*24*time.Hour {
		return 0, errors.New("bad span")
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
