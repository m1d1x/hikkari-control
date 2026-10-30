package admin

import (
	"crypto/rsa"
	"net/http"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// RequireAdmin is the only gate in front of /api/v1/admin/*.
//
// It authenticates an ADMINISTRATOR — a row in `admins` — and nothing else.
// Three things changed here when the admin plane was split off, and each one
// closed a door that led into this surface from outside it:
//
//   - The token is parsed with the ADMIN public key and must carry
//     AdminTokenAudience. An app access token fails on both counts, so a
//     user whose row happens to say role='admin' can no longer walk in with
//     the credential their chat client holds.
//   - Only the admin cookie is read. The Authorization: Bearer fallback is
//     gone: nothing in the repo ever used it, and it accepted any token the
//     app's key had signed. A machine caller, if one is ever needed, gets its
//     own credential class rather than a hole in the human one.
//   - `users.role` is not consulted at all. It means nothing to this gate.
//
// One indexed read happens per request: the session must still be live and
// belong to the token's operator (an administrator logged out or kicked stops
// at the very next request, rather than at the next poll of a cache that fails
// open), and the account must not be disabled. The same read brings the role
// and the step-up window RequireOwner and RequireStepUp decide on. Admin
// traffic is a handful of requests per second, so paying for certainty here is
// free.
func RequireAdmin(verifier *rsa.PublicKey, pool db.Pool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if verifier == nil {
			// Configuration failure, not an auth failure. Refusing loudly
			// beats falling back to some other key that would "work".
			applog.Error(r.Context(), "admin", "admin gate: no admin verifier configured")
			writeError(w, http.StatusInternalServerError, "server error")
			return
		}

		c, err := r.Cookie(auth.AdminAccessCookieName)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		claims, err := auth.ParseAdminToken(c.Value, verifier)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		st, err := auth.LoadAdminGateState(r.Context(), pool, claims.SessionID, claims.Subject)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session revoked")
			return
		}
		if st.Admin.IsDisabled {
			writeError(w, http.StatusForbidden, "admin access required")
			return
		}

		ctx := auth.WithAdmin(r.Context(), &auth.AdminInfo{
			ID:             st.Admin.ID,
			Login:          st.Admin.Login,
			DisplayName:    st.Admin.DisplayName,
			SessionID:      claims.SessionID,
			Role:           st.Admin.Role,
			SteppedUpUntil: st.SteppedUpUntil,
		})
		// Deliberately NOT applog.WithUserID: that context value feeds the
		// `user_id` column the panel's per-user log view filters on, and an
		// administrator is not a user. Putting a staff id there would make
		// "show me this user's logs" answer with a staff member's activity —
		// the same conflation the split removed everywhere else. Admin
		// attribution belongs in admin_audit_log, which records it per action.
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// RequireOwner refuses an operator whose role is not owner (task 77.4). It
// wraps a handler already behind RequireAdmin.
//
// The refusal names the reason, so the panel can say «нужна роль владельца»
// instead of a bare 403 that reads like a broken session.
func RequireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := auth.GetAdmin(r)
		if a == nil || a.Role != auth.AdminRoleOwner {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner_required"})
			return
		}
		next.ServeHTTP(w, r)
	}
}

// RequireStepUp refuses an action unless this session touched a passkey within
// the step-up window (task 77.3). It guards only what would let a stolen
// session outlive itself — a new passkey, new recovery codes, a new password —
// not moderation: a live cookie alone is what a stolen browser has; a fresh
// touch is what it does not.
//
// The panel reacts to `step_up_required` by running the touch and retrying
// the same request, so the operator sees one Touch ID prompt, not an error.
func RequireStepUp(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.GetAdmin(r).SteppedUp(time.Now()) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "step_up_required"})
			return
		}
		next.ServeHTTP(w, r)
	}
}
