package auth

// Admin-panel login.
//
// Authenticates against `admins` — a population with no relationship to
// `users` — and mints a session in `admin_sessions` signed with the admin
// plane's own keypair. Nothing in this flow touches a chat account, and the
// token it produces is not a valid chat token (admin_token.go).
//
// The refresh cookie carries the SESSION ID, not a secret: the device
// signature is what authenticates a refresh, and the secret was only ever the
// lookup key (BACKEND/22 §4).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/httputil"
)

type adminDirectLoginRequest struct {
	Login    string          `json:"username"`
	Password string          `json:"password"`
	Device   json.RawMessage `json:"device"`
}

// AdminDirectLogin handles POST /api/auth/admin/login — the password step.
// It never signs anyone in by itself: on a right password it answers with the
// second step the operator owes (admin_mfa.go).
func (h *Handlers) AdminDirectLogin(w http.ResponseWriter, r *http.Request) {
	if h.adminKeys == nil || h.adminKeys.Private == nil {
		applog.Error(r.Context(), "auth", "admin login: admin signing key not configured")
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}

	var req adminDirectLoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	login := strings.TrimSpace(req.Login)
	if login == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	binding, err := DecodeDeviceBinding(req.Device)
	if err != nil {
		writeError(w, http.StatusBadRequest, "device binding: "+err.Error())
		return
	}
	if err := binding.validate(); err != nil {
		writeError(w, http.StatusBadRequest, "device binding: "+err.Error())
		return
	}

	clientIP := httputil.ClientIP(r)

	admin, err := GetAdminByLogin(r.Context(), h.pool, login)
	if err != nil {
		if errors.Is(err, ErrAdminNotFound) {
			// Same response as a wrong password — never leak whether the
			// login exists.
			applog.Warn(r.Context(), "auth", "admin login: unknown login",
				applog.F("login", login),
				applog.F("ip", maskIP(clientIP)))
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		applog.Error(r.Context(), "auth", "admin login: lookup failed",
			applog.F("err", err.Error()),
			applog.F("login", login))
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}

	// Lockout gate, re-read separately from the row above so the freshest
	// value wins across replicas.
	if locked, until, lerr := IsAdminLocked(r.Context(), h.pool, admin.ID); lerr == nil && locked {
		retryAfter := int(time.Until(until).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		applog.Warn(r.Context(), "auth", "admin login blocked: locked",
			applog.F("admin_id", admin.ID),
			applog.F("ip", maskIP(clientIP)),
			applog.F("retry_after_s", retryAfter))
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		writeError(w, http.StatusLocked, "account_temporarily_locked")
		return
	}

	if admin.IsDisabled {
		applog.Warn(r.Context(), "auth", "admin login refused: disabled",
			applog.F("admin_id", admin.ID),
			applog.F("ip", maskIP(clientIP)))
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	if !CheckPassword(admin.PasswordHash, req.Password) {
		locked, until, ierr := IncrementAdminFailedLogin(r.Context(), h.pool, admin.ID)
		if ierr != nil {
			applog.Error(r.Context(), "auth", "admin login: failure counter update failed",
				applog.F("err", ierr.Error()),
				applog.F("admin_id", admin.ID))
		}
		applog.Warn(r.Context(), "auth", "admin login: bad password",
			applog.F("admin_id", admin.ID),
			applog.F("ip", maskIP(clientIP)),
			applog.F("locked", locked))
		if locked {
			retryAfter := int(time.Until(until).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			writeError(w, http.StatusLocked, "account_temporarily_locked")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// The password is right; it is not enough (task 77.2). No cookie leaves
	// this handler — the second step mints the session, with the device
	// binding captured HERE, so nothing the browser resends later can change
	// which device the session belongs to.
	h.beginAdminSecondFactor(w, r, admin, binding)
}

// AdminDirectLogout handles POST /api/auth/admin/logout. Wrapped in
// admin.RequireAdmin upstream, so the caller's session is already known.
func (h *Handlers) AdminDirectLogout(w http.ResponseWriter, r *http.Request) {
	if actor := GetAdmin(r); actor != nil && actor.SessionID != "" {
		if err := RevokeAdminSession(r.Context(), h.pool, actor.SessionID, "admin_logout"); err != nil {
			applog.Warn(r.Context(), "auth", "admin logout: revoke failed",
				applog.F("err", err.Error()),
				applog.F("session_id", actor.SessionID))
		}
	}
	ClearAdminAuthCookies(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
