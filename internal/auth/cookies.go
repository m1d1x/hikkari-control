package auth

// Cookie helpers for the ADMIN access + refresh pair.
//
// There used to be a second pair for the main app. It went with the auth
// routes that issued it (В9): the app authenticates in-band on the messaging
// socket, holds no cookie, and a credential nothing mints is a credential
// nothing should read.

import (
	"net/http"
	"time"
)

// Cookie names. Exported so the admin panel's code can reference them
// without string typos.
const (
	// The admin panel runs on its own origin (adminus.<base>) with its own
	// IndexedDB → its own Ed25519 device key, so it carries its own pair.
	AdminAccessCookieName  = "nekko_admin_access"
	AdminRefreshCookieName = "nekko_admin_refresh"

	// The refresh cookie is path-scoped so it never travels with ordinary
	// API requests — a smaller surface for accidental leakage through a
	// misrouted or proxied request.
	adminRefreshCookiePath = "/api/auth/admin"
)

// SetAdminAccessCookie writes the short-lived admin access JWT.
func SetAdminAccessCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminAccessCookieName,
		Value:    token,
		Path:     "/",
		Domain:   cookieDomain(r),
		MaxAge:   int(AccessTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
		Secure:   true,
	})
}

// SetAdminRefreshCookie writes the long-lived admin refresh secret. Path-
// scoped to /api/auth/admin so it only travels with admin-refresh calls.
func SetAdminRefreshCookie(w http.ResponseWriter, r *http.Request, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminRefreshCookieName,
		Value:    secret,
		Path:     adminRefreshCookiePath,
		Domain:   cookieDomain(r),
		MaxAge:   int(RefreshTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
		Secure:   true,
	})
}

// ClearAdminAuthCookies expires only the admin pair (used when an admin-
// session refresh fails so a dead session doesn't keep retrying). Main
// cookies are untouched.
func ClearAdminAuthCookies(w http.ResponseWriter, r *http.Request) {
	domain := cookieDomain(r)
	for _, c := range []*http.Cookie{
		{Name: AdminAccessCookieName, Path: "/", Domain: domain},
		{Name: AdminRefreshCookieName, Path: adminRefreshCookiePath, Domain: domain},
		{Name: AdminCSRFCookieName, Path: "/", Domain: domain},
	} {
		c.Value = ""
		c.MaxAge = -1
		c.HttpOnly = true
		c.SameSite = http.SameSiteNoneMode
		c.Secure = true
		c.Expires = time.Unix(0, 0)
		http.SetCookie(w, c)
	}
}
