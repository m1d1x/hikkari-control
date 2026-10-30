package auth

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Handlers serves the admin plane's login, second factor and step-up.
type Handlers struct {
	pool      db.Pool
	adminKeys *KeySet
	adminWA   *AdminWebAuthn

	// Outstanding device-binding challenges keyed by session id.
	challengeMu sync.Mutex
	challenges  map[string]refreshChallenge
}

// SetAdminKeys attaches the admin plane's signing keypair.
func (h *Handlers) SetAdminKeys(ks *KeySet) { h.adminKeys = ks }

// maskIP truncates a client IP to a 152-ФЗ-friendly form before it lands in
// application logs. IP-адрес — это ПДн в РФ; полный адрес в логах = риск
// при их утечке. We keep enough information to investigate abuse (geolocation,
// /24 ASN bucketing) without recording exactly which household the request
// came from.
//   - IPv4: zero out the last octet (1.2.3.4 -> 1.2.3.0).
//   - IPv6: keep the /48 prefix (matches what a typical ISP allocates).
//   - empty / unparseable: pass through unchanged so call sites still see
//     placeholder strings like "unknown" instead of misreporting them.
func maskIP(ip string) string {
	if ip == "" {
		return ip
	}
	if i := strings.LastIndex(ip, ":"); strings.Contains(ip, ":") && !strings.Contains(ip, ".") {
		// IPv6 — strictly speaking this should parse with net.ParseIP and
		// re-render, but a string-level prefix is good enough for log
		// noise reduction without dragging net into a hot helper.
		_ = i
		parts := strings.Split(ip, ":")
		if len(parts) > 3 {
			return strings.Join(parts[:3], ":") + "::"
		}
		return ip
	}
	if i := strings.LastIndex(ip, "."); i > 0 {
		return ip[:i] + ".0"
	}
	return ip
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// NewHandlers builds the admin plane handlers over the hub's database.
func NewHandlers(pool db.Pool) *Handlers {
	return &Handlers{pool: pool, challenges: make(map[string]refreshChallenge)}
}

// SetAdminWebAuthn attaches the relying party admin passkeys are verified
// with. Called once from the composition root.
func (h *Handlers) SetAdminWebAuthn(wa *AdminWebAuthn) { h.adminWA = wa }

// AdminVerifier is the public half the admin gate parses tokens with.
// Returns nil when SetAdminKeys has not run, which every caller must treat
// as "refuse", never as "skip the check".
func (h *Handlers) AdminVerifier() *rsa.PublicKey {
	if h.adminKeys == nil {
		return nil
	}
	return h.adminKeys.Public
}
