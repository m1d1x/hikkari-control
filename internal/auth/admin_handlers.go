package auth

// Admin-panel session refresh.
//
// The panel is a BROWSER and keeps cookies: П5 moved the app client onto the
// messaging socket, not this surface. What the refresh cookie CARRIES is the
// session id, not a rotating secret — the device signature below is what
// authenticates, and the secret was only ever the lookup key (BACKEND/22 §4,
// migration 131). An id is not worth stealing, so rotation and replay
// detection protect nothing that is still exposed, and both are gone.
//
// Every row this file touches belongs to the admin plane: `admin_sessions`,
// `admins`, and a token signed by the admin keypair. The cross-plane cascade
// that used to live here — main-app logout killing "admin-" prefixed rows in
// user_sessions — went with the prefix it depended on. Two populations, two
// lifecycles.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/httputil"
)

// AdminRefresh handles POST /api/auth/admin/refresh — a two-stage
// challenge/signature exchange that re-mints the admin access token.
func (h *Handlers) AdminRefresh(w http.ResponseWriter, r *http.Request) {
	if h.adminKeys == nil || h.adminKeys.Private == nil {
		applog.Error(r.Context(), "auth", "admin refresh: admin signing key not configured")
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}

	clientIP := httputil.ClientIP(r)
	ua := r.UserAgent()

	cookie, err := r.Cookie(AdminRefreshCookieName)
	if err != nil || cookie.Value == "" {
		applog.Warn(r.Context(), "auth", "admin refresh: no refresh cookie",
			applog.F("ip", maskIP(clientIP)),
			applog.F("ua", ua),
			applog.F("cookie_err", fmt.Sprintf("%v", err)))
		writeError(w, http.StatusUnauthorized, "no refresh cookie")
		return
	}

	session, lookupErr := LookupAdminSession(r.Context(), h.pool, cookie.Value)
	if lookupErr != nil {
		applog.Warn(r.Context(), "auth", "admin refresh lookup failed",
			applog.F("err", lookupErr.Error()),
			applog.F("ip", maskIP(clientIP)),
			applog.F("ua", ua))
		ClearAdminAuthCookies(w, r)
		writeError(w, http.StatusUnauthorized, "invalid refresh")
		return
	}

	// Stage 1: client just sent the cookie → issue a nonce.
	var body struct {
		Signature string `json:"signature,omitempty"`
		Nonce     string `json:"nonce,omitempty"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)

	if body.Signature == "" || body.Nonce == "" {
		nonce := make([]byte, challengeBytes)
		if _, err := rand.Read(nonce); err != nil {
			writeError(w, http.StatusInternalServerError, "server error")
			return
		}
		h.storeChallenge(session.ID, nonce)
		applog.Info(r.Context(), "auth", "admin refresh stage1 nonce issued",
			applog.F("session_id", session.ID),
			applog.F("admin_id", session.AdminID),
			applog.F("ip", maskIP(clientIP)))
		writeJSON(w, http.StatusOK, map[string]string{
			"nonce": base64.RawURLEncoding.EncodeToString(nonce),
		})
		return
	}

	// Stage 2: verify the signature against the challenge we issued.
	nonce, err := base64.RawURLEncoding.DecodeString(body.Nonce)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad nonce encoding")
		return
	}
	sig, err := decodeBase64Either(body.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad signature encoding")
		return
	}
	if !h.consumeChallenge(session.ID, nonce) {
		applog.Warn(r.Context(), "auth", "admin refresh stage2: challenge expired or unknown",
			applog.F("session_id", session.ID),
			applog.F("admin_id", session.AdminID),
			applog.F("ip", maskIP(clientIP)),
			applog.F("ua", ua))
		writeError(w, http.StatusUnauthorized, "challenge expired or unknown")
		return
	}
	if !VerifyDeviceSignature(session.PublicKey, nonce, sig) {
		applog.Warn(r.Context(), "auth", "admin refresh signature invalid",
			applog.F("session_id", session.ID),
			applog.F("admin_id", session.AdminID),
			applog.F("ip", maskIP(clientIP)))
		writeError(w, http.StatusUnauthorized, "bad signature")
		return
	}

	// The account must still be able to sign in at all. A disabled
	// administrator keeps a valid device key and a live session row, so
	// without this check they would keep re-minting access indefinitely —
	// disabling would only take effect at the session's own expiry.
	admin, err := GetAdminByID(r.Context(), h.pool, session.AdminID)
	if err != nil || admin.IsDisabled {
		reason := "admin_disabled"
		if err != nil && !errors.Is(err, ErrAdminNotFound) {
			applog.Error(r.Context(), "auth", "admin refresh: admin lookup failed",
				applog.F("err", err.Error()),
				applog.F("admin_id", session.AdminID))
			writeError(w, http.StatusInternalServerError, "server error")
			return
		}
		if err != nil {
			reason = "admin_deleted"
		}
		applog.Warn(r.Context(), "auth", "admin refresh refused",
			applog.F("session_id", session.ID),
			applog.F("admin_id", session.AdminID),
			applog.F("reason", reason))
		_ = RevokeAdminSession(r.Context(), h.pool, session.ID, reason)
		ClearAdminAuthCookies(w, r)
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	// Slide the session forward. There is nothing to rotate and therefore no
	// race to guard: the guard that matters — does this caller hold the device
	// key — just ran, against a nonce this process issued and consumed once.
	if touchErr := TouchAdminSession(r.Context(), h.pool, session.ID, clientIP, ua); touchErr != nil {
		applog.Warn(r.Context(), "auth", "admin refresh: touch session failed",
			applog.F("err", touchErr.Error()),
			applog.F("session_id", session.ID),
			applog.F("admin_id", session.AdminID),
			applog.F("ip", maskIP(clientIP)))
		writeError(w, http.StatusUnauthorized, "session race")
		return
	}

	access, err := GenerateAdminAccessToken(admin.ID, admin.Login, session.ID, AdminAccessTTL, h.adminKeys.Private)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	SetAdminAccessCookie(w, r, access)
	SetAdminRefreshCookie(w, r, session.ID)
	if _, err := IssueAdminCSRFCookie(w, r); err != nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}

	applog.Info(r.Context(), "auth", "admin refresh stage2 success — access re-minted",
		applog.F("session_id", session.ID),
		applog.F("admin_id", admin.ID),
		applog.F("ip", maskIP(clientIP)))

	writeJSON(w, http.StatusOK, map[string]string{
		"sessionId": session.ID,
	})
}
