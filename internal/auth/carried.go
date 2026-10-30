package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a plaintext password with bcrypt.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword(preHashPassword(password), 12)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword compares a bcrypt hash with a plaintext password.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), preHashPassword(password)) == nil
}

// DevicePublicKeySize is the byte length of a SEC1 uncompressed P-256 point.
// Checked at the boundary so a malformed key never reaches the verifier.
const DevicePublicKeySize = 65

// VerifyDeviceSignature reports whether sigDER is a valid signature over nonce
// by the device key pubRaw. It is the single place the curve, the hash and the
// signature encoding are decided — refresh handlers call this and nothing else,
// so there is no second copy to drift.
//
// Total function: every malformed input is a false, never a panic.
func VerifyDeviceSignature(pubRaw, nonce, sigDER []byte) bool {
	if len(pubRaw) != DevicePublicKeySize {
		return false
	}
	pub, err := ParseDevicePublicKey(pubRaw)
	if err != nil {
		return false
	}
	// The client signs the RAW nonce and lets the algorithm hash it (the
	// Secure Enclave variants hash inside the chip), so the digest is ours
	// to compute here.
	digest := sha256.Sum256(nonce)
	return ecdsa.VerifyASN1(pub, digest[:], sigDER)
}

// cookieDomain extracts the parent domain for cross-subdomain cookie sharing.
// "dev.<base>" → ".<base>", "admin.dev.<base>" → ".<base>"
// Returns empty string for localhost/IP addresses (no domain restriction).
func cookieDomain(r *http.Request) string {
	host := r.Host
	// Strip port
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	// Don't set domain for localhost or IP
	if host == "localhost" || strings.HasPrefix(host, "127.") || strings.Count(host, ".") == 3 {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return "." + parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return ""
}

// RefreshTTL is how long a session stays alive between refreshes.
// Each successful rotation slides expires_at forward by this amount, so an
// active client never logs out; an idle one drops off after RefreshTTL of
// silence. 60 days matches industry norms (Telegram, Discord) without
// granting forever-access on a cookie steal.
const RefreshTTL = 60 * 24 * time.Hour

// AccessTTL is the JWT lifetime. Short window = small post-theft blast
// radius; we accept the BG refresh load (one DB hit per client per 15 min).
const AccessTTL = 15 * time.Minute

// FingerprintHashSize is the byte length of a SHA-256 fingerprint hash; the
// client always sends 32 bytes regardless of platform.
const FingerprintHashSize = 32

// normalizeIP ensures we hand pgx an inet-friendly value or NULL. An empty
// or unparseable string becomes NULL — better than failing the whole
// insert because of a malformed X-Forwarded-For header.
//
// Post-093: kept around so the legacy INET column can still be wired with
// NULL for explicit "no value" semantics in UPDATE statements; new rows write
// the encrypted form via encryptIP and bind a NULL literal for last_ip.
func normalizeIP(ip string) any {
	if ip == "" {
		return nil
	}
	if net.ParseIP(ip) == nil {
		return nil
	}
	return ip
}

func platformOrDefault(p string) string {
	if p == "" {
		return "unknown"
	}
	return p
}

// preHashPassword sidesteps bcrypt's 72-byte input limit. bcrypt silently
// truncates anything past the 72nd byte, so two long passphrases sharing
// the first 72 bytes hash identically. We pre-hash with SHA-256 (hex-
// encoded → 64 bytes, fits comfortably) only for inputs that would
// otherwise be truncated; short passwords go through unchanged so the
// existing hash database stays valid.
func preHashPassword(password string) []byte {
	if len([]byte(password)) <= 72 {
		return []byte(password)
	}
	sum := sha256.Sum256([]byte(password))
	return []byte(hex.EncodeToString(sum[:]))
}

// ParseDevicePublicKey turns the stored raw bytes into a usable public key.
// It rejects anything that is not a well-formed point on P-256 — including the
// 32-byte Ed25519 keys written before the curve change, which is why a stale
// session row fails closed instead of verifying against garbage.
func ParseDevicePublicKey(raw []byte) (*ecdsa.PublicKey, error) {
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
}
