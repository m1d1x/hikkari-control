// Package entry is the hub's half of entry-point distribution (roadmap 42
// П6): a pool of relay records signed with entry-1, and the registry of which
// account was handed which relay.
//
// The hub holds the key and signs (sign.go, decision 2026-10-19): it IS the
// gateway, so a forged relay adds nothing to what a seized hub already has.
// What the signature still protects is a record on its way through hands that
// are not ours — DoH, public mirrors, push — and there the client checks it
// against the key compiled into it (nekko-core net::entry). A record minted by
// a seized hub is therefore valid everywhere it was copied until its own
// expires_at; nothing revokes it short of a client build that drops entry-1.
package entry

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// EntryKey1 is the public half of entry-1 — the same constant nekko-core pins
// (client-core/nekko-core/src/net/entry.rs). The private half is the hub's
// ENTRY_SIGNING_KEY (sign.go), checked against this at start.
//
// A public key, not a secret — hence the allow marker for the leak scanner.
const EntryKey1 = "24d2928b4b3be3346ae49e87dcf8b64173991df2fe77c72e7239f84bb9760188" // gitleaks:allow

// relayContext and sourcesContext are prepended to the payload before signing
// (nekko-entry, envelope.rs, Kind): a signature binds the record to its kind,
// so a valid signature on one kind cannot be offered as the other. Must match
// the Rust side byte for byte — testdata/relay.fixture.json pins both.
const (
	relayContext   = "nekko-entry/relay/v1\n"
	sourcesContext = "nekko-entry/sources/v1\n"
)

// Envelope is a signed record as nekko-entry writes it. Stored and shipped
// as-is; only the payload bytes are signed, so re-serialising it is harmless.
type Envelope struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
	Payload   string `json:"payload"`
}

// Relay is the part of a verified record the hub decides by. The rest of the
// payload (credentials, covers) is the client's business and stays inside the
// envelope.
type Relay struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Port      int    `json:"port"`
	Proto     string `json:"proto"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// Key is a public key records are believed from, under its envelope id.
type Key struct {
	ID  string
	Key ed25519.PublicKey
}

// ParseKey reads the hex form `nekko-entry keygen` prints.
func ParseKey(id, publicHex string) (Key, error) {
	raw, err := hex.DecodeString(publicHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return Key{}, fmt.Errorf("entry: public key %q is not 32 bytes of hex", id)
	}
	return Key{ID: id, Key: ed25519.PublicKey(raw)}, nil
}

// PinnedKeys is what the hub accepts records from in production.
func PinnedKeys() []Key {
	k, err := ParseKey("entry-1", EntryKey1)
	if err != nil {
		panic(err) // a malformed constant; pinned by a test
	}
	return []Key{k}
}

var (
	ErrUnknownKey   = errors.New("entry: record signed with an unknown key id")
	ErrBadSignature = errors.New("entry: record signature does not verify")
	ErrExpired      = errors.New("entry: record has expired")
	ErrMalformed    = errors.New("entry: malformed record")
)

var nameRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// openEnvelope verifies one envelope against the pinned keys under the given
// signing context and returns the payload bytes the signature covers. Mirror
// of nekko-entry envelope.rs `open`. Signature first: nothing in the payload —
// not its length, not its shape — is looked at before the key says it is ours.
func openEnvelope(env Envelope, keys []Key, context string) ([]byte, error) {
	var key *Key
	for i := range keys {
		if keys[i].ID == env.KeyID {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, env.KeyID)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("%w: payload is not base64", ErrMalformed)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: signature is not 64 bytes of base64", ErrMalformed)
	}
	msg := make([]byte, 0, len(context)+len(payload))
	msg = append(append(msg, context...), payload...)
	// The key is pinned, so the small-order-key forgeries verify_strict guards
	// against on the Rust side do not apply to it.
	if !ed25519.Verify(key.Key, msg, sig) {
		return nil, ErrBadSignature
	}
	return payload, nil
}

// Verify checks one relay record at now and returns what the hub needs of it.
// Signature first: nothing in the payload is looked at before the key says it
// is ours.
func Verify(env Envelope, keys []Key, now time.Time) (Relay, error) {
	payload, err := openEnvelope(env, keys, relayContext)
	if err != nil {
		return Relay{}, err
	}

	var r Relay
	if err := json.Unmarshal(payload, &r); err != nil {
		return Relay{}, fmt.Errorf("%w: payload is not a relay: %v", ErrMalformed, err)
	}
	// The full field rules live with the format (nekko-entry record.rs) and
	// run in the client; the hub re-checks what it keys and decides on.
	switch {
	case !nameRe.MatchString(r.Name):
		return Relay{}, fmt.Errorf("%w: name %q", ErrMalformed, r.Name)
	case r.Address == "" || r.Port <= 0 || r.Port > 65535 || r.Proto == "":
		return Relay{}, fmt.Errorf("%w: relay %s has no address, port or proto", ErrMalformed, r.Name)
	case r.IssuedAt <= 0 || r.ExpiresAt <= r.IssuedAt:
		return Relay{}, fmt.Errorf("%w: relay %s lifetime", ErrMalformed, r.Name)
	}
	if r.ExpiresAt <= now.Unix() {
		return Relay{}, fmt.Errorf("%w: relay %s at %s", ErrExpired, r.Name, time.Unix(r.ExpiresAt, 0).UTC())
	}
	return r, nil
}

// SourceListMeta is the part of a signed source list the hub keys and decides
// on: the version (issued_at, newest wins) and the lifetime. The sources
// themselves — DoH resolvers, CDN URLs — are the client's to interpret and to
// validate fully (nekko-core net::coldstart, SourceList::validate); the hub
// only relays the list it holds, so it reads no further into the payload.
type SourceListMeta struct {
	IssuedAt  int64 `json:"issued_at"`
	ExpiresAt int64 `json:"expires_at"`
}

// VerifySources checks one signed source list at now: the entry-1 signature
// over the sources context, then the lifetime. The hub's check is defence in
// depth — a corrupted upload, a list signed by anybody else, or an expired one
// never reaches the pool, let alone a client — not the trust root; the client
// verifies again (nekko-core net::entry_plane). The signed context differs
// from a relay's, so a relay record offered as a source list fails here.
func VerifySources(env Envelope, keys []Key, now time.Time) (SourceListMeta, error) {
	payload, err := openEnvelope(env, keys, sourcesContext)
	if err != nil {
		return SourceListMeta{}, err
	}
	var m SourceListMeta
	if err := json.Unmarshal(payload, &m); err != nil {
		return SourceListMeta{}, fmt.Errorf("%w: payload is not a source list: %v", ErrMalformed, err)
	}
	if m.IssuedAt <= 0 || m.ExpiresAt <= m.IssuedAt {
		return SourceListMeta{}, fmt.Errorf("%w: source list lifetime", ErrMalformed)
	}
	if m.ExpiresAt <= now.Unix() {
		return SourceListMeta{}, fmt.Errorf("%w: source list at %s", ErrExpired, time.Unix(m.ExpiresAt, 0).UTC())
	}
	return m, nil
}
