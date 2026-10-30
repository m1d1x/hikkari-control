// Package cryptobox is the application-level encryption helper for sensitive
// columns: messages, drafts, notification payloads, IP addresses. The goal
// is "encryption at rest with a server-held key" — Postgres only ever sees
// ciphertext, the master key lives in the Go process's environment.
//
// Threat model:
//   - pg_dump leak / SQL injection / compromised DBA → ciphertext only,
//     no plaintext recoverable without the env-var key.
//   - Compromised running app server → key is in process memory; the
//     attacker who already has root on the box can decrypt. This is the
//     accepted limit; full E2E requires client-held keys, which we
//     explicitly aren't doing.
//
// Algorithm: AES-256-GCM. The standard for AEAD; AES-NI on every x86_64
// produced in the last decade keeps it fast (1-3 GB/s per core).
//
// Envelope format:
//
//	byte 0          version tag (currently 0x01 for AES-256-GCM v1)
//	byte 1          key version (1..255). Lets us rotate keys later
//	                without reading-then-rewriting every old row at once.
//	bytes 2..13     nonce (12 bytes, randomly generated per encrypt)
//	bytes 14..N-17  ciphertext
//	bytes N-16..N   GCM auth tag (appended by the standard library)
//
// Decoders fan out by version tag/key-version. This keeps the format
// extensible without cuts to existing data.
package cryptobox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	// envelopeVersionAESGCM marks the v1 layout above.
	envelopeVersionAESGCM byte = 0x01
	// keySize is the AES-256 key length in bytes.
	keySize = 32
	// nonceSize is the recommended GCM nonce length.
	nonceSize = 12
	// MinEnvelopeSize is "version + keyver + nonce + tag" — 30 bytes is
	// the floor for the empty plaintext case.
	MinEnvelopeSize = 1 + 1 + nonceSize + 16
)

// ErrNotConfigured is returned when no key has been registered. Callers
// that hit this on a write path SHOULD fail loud — silently storing
// plaintext re-opens the privacy gap the package exists to close.
var ErrNotConfigured = errors.New("cryptobox: no key configured (set NEKKO_CONTENT_KEY)")

// ErrCiphertextMalformed indicates the input is not a recognised
// envelope. Most likely culprit is data corruption or a row written by a
// future format the running binary doesn't know.
var ErrCiphertextMalformed = errors.New("cryptobox: ciphertext malformed")

// ErrKeyVersionUnknown means the row was encrypted with a key version we
// no longer load. Operators must keep retired keys in env until backfill
// has rewritten every row to the latest version.
var ErrKeyVersionUnknown = errors.New("cryptobox: key version not loaded")

// box is the singleton holding all loaded keys. We reference it through
// package-level helpers (Encrypt/Decrypt) so call sites don't pass it
// around — the pool is process-wide and conceptually a single capability.
type box struct {
	mu sync.RWMutex
	// aeads maps key-version -> ready-to-use AEAD. We pre-build the
	// cipher.AEAD instead of NewGCM-ing on every call: each construction
	// allocates and runs key schedule, avoidable on the hot path.
	aeads map[byte]cipher.AEAD
	// roots maps key-version -> HKDF pseudorandom key for [Derive]. Kept
	// instead of the raw key, so the content key itself lives only inside its
	// AEAD.
	roots map[byte][]byte
	// activeVersion is the key version used for new encryptions. Older
	// versions stay loaded so existing rows decrypt; bumping the active
	// version on a fresh deploy makes future writes use the new key.
	activeVersion byte
}

var defaultBox = &box{aeads: make(map[byte]cipher.AEAD), roots: make(map[byte][]byte)}

// Configure wires keys from environment variables. Call this once at
// startup, before any code reads/writes encrypted columns.
//
// Required: NEKKO_CONTENT_KEY = base64(32 random bytes). This is the
// active key.
//
// Optional: NEKKO_CONTENT_KEY_V<N> = base64(32 bytes), where N is 1..255.
// These are old keys retained for decrypting legacy rows during a key
// rotation. NEKKO_CONTENT_KEY itself is treated as version 1 by default;
// to rotate, set NEKKO_CONTENT_KEY to the new key, copy the old key to
// NEKKO_CONTENT_KEY_V1 (so v1 rows still decrypt), and set
// NEKKO_CONTENT_KEY_VERSION=2 so new writes use the new version.
//
// Returns an error only when the active key is missing or malformed —
// that's a fatal misconfiguration the binary should not start without.
func Configure(env func(string) string) error {
	if env == nil {
		env = os.Getenv
	}
	activeRaw := strings.TrimSpace(env("NEKKO_CONTENT_KEY"))
	if activeRaw == "" {
		return ErrNotConfigured
	}
	activeKey, err := decodeKey(activeRaw)
	if err != nil {
		return fmt.Errorf("NEKKO_CONTENT_KEY: %w", err)
	}

	// Active version defaults to 1 unless the operator set it explicitly.
	activeVersion := byte(1)
	if v := strings.TrimSpace(env("NEKKO_CONTENT_KEY_VERSION")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 255 {
			return fmt.Errorf("NEKKO_CONTENT_KEY_VERSION must be 1..255, got %q", v)
		}
		activeVersion = byte(n)
	}

	defaultBox.mu.Lock()
	defer defaultBox.mu.Unlock()
	defaultBox.aeads = make(map[byte]cipher.AEAD)
	defaultBox.roots = make(map[byte][]byte)
	defaultBox.activeVersion = activeVersion

	if err := registerLocked(defaultBox, activeVersion, activeKey); err != nil {
		return err
	}

	// Load any legacy keys (NEKKO_CONTENT_KEY_V1, _V2, ...). Skip the
	// active version slot so a stale env var can't shadow the active key.
	for n := 1; n <= 255; n++ {
		if byte(n) == activeVersion {
			continue
		}
		raw := strings.TrimSpace(env(fmt.Sprintf("NEKKO_CONTENT_KEY_V%d", n)))
		if raw == "" {
			continue
		}
		k, err := decodeKey(raw)
		if err != nil {
			return fmt.Errorf("NEKKO_CONTENT_KEY_V%d: %w", n, err)
		}
		if err := registerLocked(defaultBox, byte(n), k); err != nil {
			return err
		}
	}
	return nil
}

// IsConfigured reports whether the package has at least one usable key.
// Useful for boot-time checks that want to bail out before the first
// write hits ErrNotConfigured.
func IsConfigured() bool {
	defaultBox.mu.RLock()
	defer defaultBox.mu.RUnlock()
	return len(defaultBox.aeads) > 0
}

// ActiveKeyVersion returns the version stamp current writes use. Mostly
// useful for diagnostics / audit logs.
func ActiveKeyVersion() byte {
	defaultBox.mu.RLock()
	defer defaultBox.mu.RUnlock()
	return defaultBox.activeVersion
}

// Encrypt seals the plaintext with the active key. Output is a valid
// envelope (see package doc) suitable for direct storage in a BYTEA
// column.
//
// Empty plaintext returns an empty ([]byte{}) envelope — by convention
// "no value" stays as zero-length on the wire so callers don't have to
// special-case nil round-tripping.
func Encrypt(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return []byte{}, nil
	}
	defaultBox.mu.RLock()
	aead, ok := defaultBox.aeads[defaultBox.activeVersion]
	version := defaultBox.activeVersion
	defaultBox.mu.RUnlock()
	if !ok {
		return nil, ErrNotConfigured
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("cryptobox: nonce: %w", err)
	}

	out := make([]byte, 0, 2+nonceSize+len(plaintext)+aead.Overhead())
	out = append(out, envelopeVersionAESGCM, version)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// EncryptString is a convenience wrapper for the common string case.
func EncryptString(s string) ([]byte, error) {
	return Encrypt([]byte(s))
}

// Decrypt reverses Encrypt. Empty envelope decodes to empty plaintext
// (no allocation) so the caller can treat NULL/empty columns uniformly.
func Decrypt(envelope []byte) ([]byte, error) {
	if len(envelope) == 0 {
		return []byte{}, nil
	}
	if len(envelope) < MinEnvelopeSize {
		return nil, ErrCiphertextMalformed
	}
	if envelope[0] != envelopeVersionAESGCM {
		return nil, fmt.Errorf("%w: unknown version 0x%02x", ErrCiphertextMalformed, envelope[0])
	}
	keyVersion := envelope[1]
	defaultBox.mu.RLock()
	aead, ok := defaultBox.aeads[keyVersion]
	defaultBox.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrKeyVersionUnknown, keyVersion)
	}
	nonce := envelope[2 : 2+nonceSize]
	body := envelope[2+nonceSize:]
	plaintext, err := aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: open: %w", err)
	}
	return plaintext, nil
}

// DecryptString is the EncryptString counterpart.
func DecryptString(envelope []byte) (string, error) {
	b, err := Decrypt(envelope)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// GenerateKey returns a freshly-randomized 32-byte key encoded as
// base64. Used by the keygen subcommand and tests.
func GenerateKey() (string, error) {
	k := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return "", fmt.Errorf("cryptobox: rand: %w", err)
	}
	return base64.StdEncoding.EncodeToString(k), nil
}

// decodeKey parses one of the supported key encodings. We accept both
// std and url-safe base64 with or without padding so a key copied out of
// a password manager survives whichever variant it stored.
func decodeKey(raw string) ([]byte, error) {
	encs := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, e := range encs {
		if k, err := e.DecodeString(raw); err == nil {
			if len(k) != keySize {
				return nil, fmt.Errorf("expected %d bytes after decode, got %d", keySize, len(k))
			}
			return k, nil
		} else {
			lastErr = err
		}
	}
	return nil, fmt.Errorf("not valid base64: %w", lastErr)
}

// registerLocked installs an AEAD for the given key version. Caller
// holds box.mu.
func registerLocked(b *box, version byte, key []byte) error {
	if version == 0 {
		return errors.New("cryptobox: key version 0 reserved")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("cryptobox: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("cryptobox: gcm: %w", err)
	}
	root, err := hkdf.Extract(sha256.New, key, []byte(deriveSalt))
	if err != nil {
		return fmt.Errorf("cryptobox: hkdf: %w", err)
	}
	b.aeads[version] = aead
	b.roots[version] = root
	return nil
}

// deriveSalt separates [Derive] output from every other use of the content
// key: what comes out of it is never an AES-GCM key of this package.
const deriveSalt = "github.com/m1d1x/hikkari-control/cryptobox/derive/v1"

// Derive returns a 32-byte server-held secret for `label`, from the ACTIVE
// content key (HKDF-SHA256). For keyed functions that need a secret of the
// same standing as the content key without an env var of their own — e.g.
// the entry-batch HMAC (internal/entry). Distinct labels give unrelated keys.
//
// ⚠️ Rotating the content key changes every derived key. Use it only where
// that is harmless: a value that must stay readable belongs in Encrypt.
func Derive(label string) ([]byte, error) {
	if label == "" {
		return nil, errors.New("cryptobox: derive needs a label")
	}
	defaultBox.mu.RLock()
	root, ok := defaultBox.roots[defaultBox.activeVersion]
	defaultBox.mu.RUnlock()
	if !ok {
		return nil, ErrNotConfigured
	}
	return hkdf.Expand(sha256.New, root, label, keySize)
}

// resetForTests wipes loaded keys. Test-only helper.
func resetForTests() {
	defaultBox.mu.Lock()
	defaultBox.aeads = make(map[byte]cipher.AEAD)
	defaultBox.roots = make(map[byte][]byte)
	defaultBox.activeVersion = 0
	defaultBox.mu.Unlock()
}

// ConfigureEphemeralForTests loads a fresh random content key. It exists for
// the TestMain of packages whose code writes sealed rows — every durable event
// is sealed (messaging.AppendEvent), so any package that records one needs a
// key loaded before its first test. Never called by a binary: a random key at
// boot would leave every restart unable to read what the previous one wrote.
func ConfigureEphemeralForTests() {
	key, err := GenerateKey()
	if err != nil {
		panic(err)
	}
	if err := Configure(func(name string) string {
		if name == "NEKKO_CONTENT_KEY" {
			return key
		}
		return ""
	}); err != nil {
		panic(err)
	}
}
