package auth

// Access tokens for the ADMIN plane.
//
// Deliberately a separate file, a separate claims type, a separate audience
// and — through LoadAdminKeySet — a separate signing key from the app's
// access token in auth.go. Until this existed, the panel's token was minted
// by GenerateAccessToken with AccessTokenAudience: byte for byte an ordinary
// chat session token that happened to be delivered in a different cookie. One
// stolen credential therefore opened both surfaces, in whichever direction it
// was stolen.
//
// The subject here is an `admins.id`, never a `users.id`. An administrator is
// not a participant of the product, so nothing in these claims can be handed
// to a user-facing lookup by accident: the types do not line up (AdminClaims
// vs Claims) and neither do the keys.

import (
	"context"
	"crypto/rsa"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// AdminTokenAudience and AdminTokenIssuer pin an admin token to the admin
	// plane. Checked on every parse (ParseAdminToken) on top of the separate
	// keypair — two independent reasons a token from anywhere else fails.
	AdminTokenAudience = "nekko-admin-access"
	AdminTokenIssuer   = "nekko-admin"

	// AdminWSTicketAudience tags the short-lived ticket the panel redeems to
	// upgrade a WebSocket (the live log stream). Domain-separated from
	// AdminTokenAudience for the same reason WSTicketAudience is separated
	// from AccessTokenAudience: a ticket travels in a query string, where it
	// lands in access logs and Referer headers, so it must not be replayable
	// as a session.
	AdminWSTicketAudience = "nekko-admin-ws-ticket"
)

// AdminAccessTTL is how long an admin access token is good for.
//
// Shorter than the app's AccessTTL: the panel refreshes proactively on a timer
// (admin/src/lib/auth/keepalive.ts) and every mutating call it makes is one
// that a stolen token would be able to make too.
const AdminAccessTTL = 10 * time.Minute

// AdminClaims is the admin plane's token body.
//
// `Login` is carried for logging and for the panel's own "signed in as" line;
// it is NOT authorisation. Authorisation is re-read from the admins row on
// every request, so disabling an account takes effect within one access TTL
// rather than whenever the holder happens to refresh.
type AdminClaims struct {
	jwt.RegisteredClaims
	Login     string `json:"login"`
	SessionID string `json:"sid,omitempty"`
}

// GenerateAdminAccessToken mints an admin access token for one admin session.
//
// adminID is an admins.id; sessionID is an admin_sessions.id.
func GenerateAdminAccessToken(adminID, login, sessionID string, ttl time.Duration, key *rsa.PrivateKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("nil admin private key")
	}
	claims := AdminClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   adminID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Audience:  jwt.ClaimStrings{AdminTokenAudience},
			Issuer:    AdminTokenIssuer,
		},
		Login:     login,
		SessionID: sessionID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

// ParseAdminToken validates an admin access token with the admin public key.
//
// RS256 only, and audience + issuer enforced — the same three guards
// ParseToken applies, against a different key and a different audience. A
// token minted for the app, for a ws-ticket, or for email verification fails
// all three.
func ParseAdminToken(tokenStr string, key *rsa.PublicKey) (*AdminClaims, error) {
	return parseAdminJWT(tokenStr, key, AdminTokenAudience, jwt.WithIssuer(AdminTokenIssuer))
}

// GenerateAdminWSTicket mints the short-lived single-use ticket the panel
// exchanges for a WebSocket upgrade. No issuer claim, matching ParseWSTicket's
// shape for the app side.
func GenerateAdminWSTicket(adminID, login, sessionID string, ttl time.Duration, key *rsa.PrivateKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("nil admin private key")
	}
	claims := AdminClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   adminID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Audience:  jwt.ClaimStrings{AdminWSTicketAudience},
		},
		Login:     login,
		SessionID: sessionID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

// ParseAdminWSTicket validates a ticket minted by GenerateAdminWSTicket.
func ParseAdminWSTicket(tokenStr string, key *rsa.PublicKey) (*AdminClaims, error) {
	return parseAdminJWT(tokenStr, key, AdminWSTicketAudience)
}

func parseAdminJWT(tokenStr string, key *rsa.PublicKey, audience string, extra ...jwt.ParserOption) (*AdminClaims, error) {
	if key == nil {
		return nil, fmt.Errorf("nil admin public key")
	}
	opts := append([]jwt.ParserOption{jwt.WithAudience(audience)}, extra...)
	token, err := jwt.ParseWithClaims(tokenStr, &AdminClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return key, nil
	}, opts...)
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*AdminClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid admin token")
	}
	return claims, nil
}

// AdminInfo is the authenticated administrator, as the gate puts it into the
// request context.
//
// A distinct type from UserInfo on purpose. The two are never interchangeable,
// and making them different types means a handler that reaches for the wrong
// one does not compile — rather than compiling and attributing a staff action
// to a chat account, which is precisely the confusion this plane was split to
// end.
type AdminInfo struct {
	ID          string // admins.id
	Login       string
	DisplayName string
	SessionID   string // admin_sessions.id
	Role        AdminRole
	// SteppedUpUntil is when this session's last fresh passkey touch stops
	// authorizing destructive actions; nil when it never stepped up.
	SteppedUpUntil *time.Time
}

// SteppedUp reports whether the session may perform a destructive action now.
func (a *AdminInfo) SteppedUp(now time.Time) bool {
	return a != nil && a.SteppedUpUntil != nil && a.SteppedUpUntil.After(now)
}

type adminContextKeyType struct{}

// AdminContextKey is where the admin gate stores the AdminInfo.
var AdminContextKey = adminContextKeyType{}

// WithAdmin returns a context carrying the authenticated administrator.
func WithAdmin(ctx context.Context, a *AdminInfo) context.Context {
	return context.WithValue(ctx, AdminContextKey, a)
}

// GetAdmin returns the administrator the gate authenticated, or nil. A nil
// result means the request never passed the gate and the handler must not act.
func GetAdmin(r *http.Request) *AdminInfo {
	a, _ := r.Context().Value(AdminContextKey).(*AdminInfo)
	return a
}
