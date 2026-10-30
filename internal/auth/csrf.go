package auth

// Double-submit CSRF token.
//
// Cookies in this project are SameSite=None+Secure (frontend, api and admin
// live on different subdomains and must share auth across origins), which
// means the browser's built-in CSRF protection is off. We layer a
// double-submit token on top: server sets a non-HttpOnly nekko_csrf cookie,
// frontend reads it via document.cookie and echoes it back as
// X-CSRF-Token. Middleware compares header to cookie in constant time.
// An attacker on a foreign origin can ride cookies but cannot read them
// (no CORS allow-credentials from their origin), so they can't fill the
// header.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

const (
	// AdminCSRFCookieName is the double-submit cookie for the admin panel —
	// cookie because admin lives on its own origin with its own access
	// pair, and we don't want a stale main-app csrf to validate an admin
	// request after a main-app logout.
	AdminCSRFCookieName = "nekko_admin_csrf"

	// CSRFHeaderName is what the frontend echoes on every mutating
	// request. Lowercase comparison is handled by net/http.
	CSRFHeaderName = "X-CSRF-Token"

	// csrfTokenBytes is the entropy of the token. 32 bytes = 256 bits,
	// well above what a guessing attack could hit before the cookie
	// rotates on next refresh.
	csrfTokenBytes = 32
)

// GenerateCSRFToken returns a fresh URL-safe base64 token suitable for
// the csrf cookie + X-CSRF-Token header.
func GenerateCSRFToken() (string, error) {
	buf := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// SetAdminCSRFCookie is the admin-panel equivalent of SetCSRFCookie.
func SetAdminCSRFCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminCSRFCookieName,
		Value:    token,
		Path:     "/",
		Domain:   cookieDomain(r),
		MaxAge:   int(RefreshTTL.Seconds()),
		HttpOnly: false,
		SameSite: http.SameSiteNoneMode,
		Secure:   true,
	})
}

// IssueAdminCSRFCookie is the admin equivalent of IssueCSRFCookie.
func IssueAdminCSRFCookie(w http.ResponseWriter, r *http.Request) (string, error) {
	tok, err := GenerateCSRFToken()
	if err != nil {
		return "", err
	}
	SetAdminCSRFCookie(w, r, tok)
	return tok, nil
}

// CSRFTokensMatch is a constant-time equality check for the cookie value
// and the X-CSRF-Token header. Both must be non-empty AND equal — empty
// strings never match (defends against the "no cookie / no header"
// trivial bypass).
func CSRFTokensMatch(cookieValue, headerValue string) bool {
	if cookieValue == "" || headerValue == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookieValue), []byte(headerValue)) == 1
}

// AdminCSRFToken handles GET /api/auth/admin/csrf: hand the panel a
// double-submit token BEFORE it has a session.
//
// Without this the login POST is unprotectable. The CSRF middleware demands a
// header matching a cookie, and on a cold panel there is no cookie yet — so
// the only alternatives are exempting the login route (which leaves login-CSRF
// open: an attacker's cross-site POST signs the operator's browser into an
// account the attacker controls, and the operator then works inside it) or
// this: a token minted on a safe method, which the panel echoes on the login
// request like any other mutating call.
//
// Safe method, so the middleware lets it through without a token of its own.
// It authenticates nobody and carries no secret: the value's only job is to be
// unreadable from a foreign origin, which same-origin policy and our CORS
// allow-list already guarantee.
func AdminCSRFToken(w http.ResponseWriter, r *http.Request) {
	if _, err := IssueAdminCSRFCookie(w, r); err != nil {
		http.Error(w, `{"error":"server error"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
