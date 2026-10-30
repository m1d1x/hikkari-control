package auth

// device_binding.go is the device half of a session: the P-256 key a client
// registers, and the one-shot nonce that proves it still holds the private
// half when a session is refreshed.
//
// The file was called sessions_handlers.go and held five REST handlers —
// refresh, list devices, kick one, kick all, plus the cookie-writing
// IssueSession they shared. All five had a twin operation on the messaging
// socket and no callers, and went with В9. What is left is what BOTH the wire
// and the admin refresh still need: decode a binding, validate it, hand out a
// challenge and consume it exactly once.

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/m1d1x/hikkari-control/internal/devicecard"
)

// challengeTTL is how long a server-issued nonce stays valid. Short enough
// that a leaked challenge can't be sat on; long enough to survive client
// processing + a slow uplink.
const challengeTTL = 30 * time.Second

// challengeBytes is the entropy of one challenge nonce. 32 bytes = no
// chance of accidental collision across concurrent refresh attempts.
const challengeBytes = 32

// refreshChallenge is the one-shot binding between an issued nonce and the
// session it's expected to authenticate. Stored in-process (handlers struct)
// because challenges are seconds-scale; pushing them to Postgres would
// double the per-refresh DB load for no security gain.
type refreshChallenge struct {
	sessionID string
	nonce     []byte
	expiresAt time.Time
}

// DeviceBinding is the per-request data the client sends so we can
// remember the device. Decoded from JSON by the admin login/refresh path;
// the wire decodes its own protobuf shape into the same struct.
type DeviceBinding struct {
	// PublicKey is the SEC1 uncompressed P-256 point (65 bytes). Sent
	// base64url in JSON, decoded in DecodeDeviceBinding.
	PublicKey []byte
	// FingerprintHash is sha256(normalized device attrs). 32 bytes.
	FingerprintHash []byte
	// Platform is a UI label like "macos" / "web-chrome".
	Platform string
	// Card is the model and system the device reports for its owner's
	// sessions list. Stored encrypted under the server key.
	Card devicecard.Card
	//
	// There used to be an `IsTrusted` flag beside it, set for clients with
	// OS-level secure storage. It is gone (migration 131): Electron is
	// archived, and once the key moves into a secure element (doc 17 Ш3)
	// "trusted" becomes a property of the KEY STORE — software / TEE /
	// StrongBox / Secure Enclave — which is an enumeration about attestation,
	// not a boolean about browsers.
}

func (b DeviceBinding) validate() error {
	if len(b.PublicKey) != DevicePublicKeySize {
		return fmt.Errorf("public_key must be %d bytes", DevicePublicKeySize)
	}
	if len(b.FingerprintHash) != FingerprintHashSize {
		return fmt.Errorf("fingerprint_hash must be %d bytes", FingerprintHashSize)
	}
	if _, err := devicecard.Normalize(b.Card); err != nil {
		return err
	}
	return nil
}

// deviceBindingJSON mirrors the JSON shape sent by clients. Kept private —
// callers go through DecodeDeviceBinding so we control the base64 +
// validation in one place.
type deviceBindingJSON struct {
	PublicKey       string `json:"publicKey"`
	FingerprintHash string `json:"fingerprintHash"`
	Platform        string `json:"platform"`
}

// DecodeDeviceBinding parses the device fields from a JSON object. Both
// PublicKey and FingerprintHash arrive as URL-safe base64; we also
// tolerate standard base64 in case a non-Web client uses a stricter
// encoder.
func DecodeDeviceBinding(raw json.RawMessage) (DeviceBinding, error) {
	if len(raw) == 0 {
		return DeviceBinding{}, errors.New("device binding missing")
	}
	var j deviceBindingJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return DeviceBinding{}, fmt.Errorf("decode device binding: %w", err)
	}
	pub, err := decodeBase64Either(j.PublicKey)
	if err != nil {
		return DeviceBinding{}, fmt.Errorf("publicKey: %w", err)
	}
	fp, err := decodeBase64Either(j.FingerprintHash)
	if err != nil {
		return DeviceBinding{}, fmt.Errorf("fingerprintHash: %w", err)
	}
	return DeviceBinding{
		PublicKey:       pub,
		FingerprintHash: fp,
		Platform:        j.Platform,
	}, nil
}

// storeChallenge / consumeChallenge keep one outstanding nonce per session
// in process memory. We don't need a global map — one entry per refresh
// flow, expires in seconds. Caller-side mutex is on Handlers, declared in
// handlers_state.go alongside the field itself.
func (h *Handlers) storeChallenge(sessionID string, nonce []byte) {
	h.challengeMu.Lock()
	defer h.challengeMu.Unlock()
	h.purgeExpiredChallengesLocked()
	h.challenges[sessionID] = refreshChallenge{
		sessionID: sessionID,
		nonce:     nonce,
		expiresAt: time.Now().Add(challengeTTL),
	}
}

// consumeChallenge returns true iff the (sessionID, nonce) pair matches
// an unexpired challenge AND deletes it (single-use).
func (h *Handlers) consumeChallenge(sessionID string, nonce []byte) bool {
	h.challengeMu.Lock()
	defer h.challengeMu.Unlock()
	c, ok := h.challenges[sessionID]
	if !ok {
		return false
	}
	if time.Now().After(c.expiresAt) {
		delete(h.challenges, sessionID)
		return false
	}
	if subtle.ConstantTimeCompare(c.nonce, nonce) != 1 {
		return false
	}
	delete(h.challenges, sessionID)
	return true
}

func (h *Handlers) purgeExpiredChallengesLocked() {
	now := time.Now()
	for k, v := range h.challenges {
		if now.After(v.expiresAt) {
			delete(h.challenges, k)
		}
	}
}

// decodeBase64Either accepts either RawURLEncoding (Web subtle.exportKey)
// or StdEncoding (Go base64.StdEncoding). Tries URL-safe first because
// that's what frontend uses; falls back so non-browser clients don't have
// to special-case.
func decodeBase64Either(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
