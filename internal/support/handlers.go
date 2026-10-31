package support

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

const maxReportBody = 2 << 20 // 2MB

// RegisterPublicRoutes mounts client-facing support endpoints.
func RegisterPublicRoutes(mux *http.ServeMux, store Store, limiter *Limiter) {
	mux.HandleFunc("POST /api/v1/support/report", ReportHandler(store, limiter))
}

// RegisterAdminRoutes mounts operator-facing support endpoints.
// gate applies admin authentication/authorization.
func RegisterAdminRoutes(mux *http.ServeMux, store Store, gate func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/support/reports", gate(ListHandler(store)))
	mux.HandleFunc("GET /api/v1/admin/support/reports/{id}", gate(GetHandler(store)))
	mux.HandleFunc("POST /api/v1/admin/support/reports/{id}/reply", gate(ReplyHandler(store)))
	mux.HandleFunc("PATCH /api/v1/admin/support/reports/{id}", gate(StatusHandler(store)))
	mux.HandleFunc("POST /api/v1/admin/support/reports/{id}/status", gate(StatusHandler(store)))
}

// ReportHandler handles diagnostic report submissions from clients.
func ReportHandler(store Store, limiter *Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ReportRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBody)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}

		req.InstallID = strings.TrimSpace(req.InstallID)
		req.Platform = strings.TrimSpace(req.Platform)
		req.Version = strings.TrimSpace(req.Version)
		req.Message = strings.TrimSpace(req.Message)

		if req.InstallID == "" {
			writeErr(w, http.StatusBadRequest, "install_id is required")
			return
		}
		if req.Message == "" {
			writeErr(w, http.StatusBadRequest, "message is required")
			return
		}
		if req.Platform == "" {
			req.Platform = "unknown"
		}
		if req.Version == "" {
			req.Version = "unknown"
		}

		if limiter != nil && !limiter.Allow(req.InstallID) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		rep, err := store.CreateReport(r.Context(), req)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to save report")
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"ok":     true,
			"id":     rep.ID,
			"status": rep.Status,
		})
	}
}

// ListHandler lists support reports, optionally filtered by status.
func ListHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		reports, err := store.ListReports(r.Context(), status)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to list reports")
			return
		}
		if reports == nil {
			reports = []*Report{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"reports": reports,
		})
	}
}

// GetHandler retrieves a single support report by ID.
func GetHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid report id")
			return
		}

		rep, err := store.GetReport(r.Context(), id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				writeErr(w, http.StatusNotFound, "report not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, "failed to get report")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"report": rep,
		})
	}
}

// ReplyHandler appends a staff reply to a support report.
func ReplyHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid report id")
			return
		}

		var req ReplyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}

		text := strings.TrimSpace(req.Text)
		if text == "" {
			writeErr(w, http.StatusBadRequest, "reply text is required")
			return
		}

		rep, err := store.AddReply(r.Context(), id, text)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				writeErr(w, http.StatusNotFound, "report not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, "failed to add reply")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"report": rep,
		})
	}
}

// StatusHandler updates the status of a report.
func StatusHandler(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid report id")
			return
		}

		var req StatusRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}

		status := strings.TrimSpace(req.Status)
		rep, err := store.UpdateStatus(r.Context(), id, status)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				writeErr(w, http.StatusNotFound, "report not found")
				return
			}
			if errors.Is(err, ErrInvalidStatus) {
				writeErr(w, http.StatusBadRequest, "invalid status")
				return
			}
			writeErr(w, http.StatusInternalServerError, "failed to update status")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"report": rep,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": message,
	})
}
