package entry

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
)

// The hub signs (roadmap 42 П6.15, decision 2026-10-19). entry-1 used to live
// offline on the owner's machine; it moved here because the hub IS the gateway:
// whoever holds it already holds the clients' addresses, the TLS key and every
// piece of metadata, so a forged relay on top adds nothing. The signature
// still matters where a record travels through hands that are not ours — DoH,
// public mirrors, push (П6.5, П6.8) — and there the client checks it against
// the key compiled into it (nekko-core net::entry).

// The protocols a relay record names, spelled as nekko-entry record.rs
// serialises Params: two stream entries — Reality (П5) and XHTTP inside
// ordinary TLS on the relay's own domain (П4в) — and a voice entry in front of
// one region's SFUs (voice plan Ф3).
const (
	ProtoVlessRealityVision = "vless-reality-vision"
	ProtoHysteria2          = "hysteria2"
	ProtoVlessXhttp         = "vless-xhttp"
)

// XHTTP modes (record.rs XhttpMode). StreamUp is the default: the edge
// terminates TLS itself, nothing on the path buffers a streaming upload.
const (
	XhttpPacketUp  = "packet-up"
	XhttpStreamUp  = "stream-up"
	XhttpStreamOne = "stream-one"
)

// SigningKeyEnv names the env var with the hex seed of entry-1 (what
// `nekko-entry keygen` writes).
const SigningKeyEnv = "ENTRY_SIGNING_KEY"

// ErrNoSigner: the hub runs without ENTRY_SIGNING_KEY and cannot admit relays
// or publish a source list. Everything else about the pool still works.
var ErrNoSigner = errors.New("entry: no signing key (set " + SigningKeyEnv + ")")

// Signer makes envelopes a client accepts.
type Signer struct {
	id  string
	key ed25519.PrivateKey
}

// LoadSigner reads entry-1 from the environment. Unset is not an error (nil
// signer: the pool serves, it cannot sign); set but wrong is — a hub signing
// under a key no client pins would hand out records every client throws away.
func LoadSigner(getenv func(string) string, pinned []Key) (*Signer, error) {
	raw := strings.TrimSpace(getenv(SigningKeyEnv))
	if raw == "" {
		return nil, nil
	}
	seed, err := hex.DecodeString(raw)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("entry: %s is not 32 bytes of hex", SigningKeyEnv)
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	for _, k := range pinned {
		if k.Key.Equal(pub) {
			return &Signer{id: k.ID, key: key}, nil
		}
	}
	return nil, fmt.Errorf("entry: %s is not the private half of any pinned key", SigningKeyEnv)
}

// Key is the public half the signer signs under, to open what it signed.
func (s *Signer) Key() Key {
	return Key{ID: s.id, Key: s.key.Public().(ed25519.PublicKey)}
}

// NewSigner is for tests: sign under an explicit id and seed.
func NewSigner(id string, seed []byte) *Signer {
	return &Signer{id: id, key: ed25519.NewKeyFromSeed(seed)}
}

// RelayRecord is the whole signed payload of a relay — nekko-entry record.rs
// Relay, field for field. The protocol's parameters ride flat beside the
// common fields, exactly one of the halves set, the one Proto names. The
// client validates it fully; the hub builds it from an identity it generated
// itself, and the cross-language fixture (testdata/hub-relay.fixture.json)
// pins that the client accepts what it builds.
//
// The halves are not embedded: Reality and XHTTP both have a "uuid", and
// encoding/json silently drops a name two embedded structs share. MarshalJSON
// flattens the one half Proto names instead.
type RelayRecord struct {
	Name           string
	Address        string
	Port           int
	Proto          string
	RealityParams  *RealityParams
	HysteriaParams *HysteriaParams
	XhttpParams    *XhttpParams
	IssuedAt       int64
	ExpiresAt      int64
}

// params is the half Proto names, nil when Proto is unknown.
func (r RelayRecord) params() any {
	switch r.Proto {
	case ProtoVlessRealityVision:
		return r.RealityParams
	case ProtoHysteria2:
		return r.HysteriaParams
	case ProtoVlessXhttp:
		return r.XhttpParams
	}
	return nil
}

// MarshalJSON writes the common fields (Relay, what Verify reads back) and
// the half Proto names, flat.
func (r RelayRecord) MarshalJSON() ([]byte, error) {
	if err := r.halves(); err != nil {
		return nil, err
	}
	doc := map[string]json.RawMessage{}
	for _, part := range []any{Relay{r.Name, r.Address, r.Port, r.Proto, r.IssuedAt, r.ExpiresAt}, r.params()} {
		raw, err := json.Marshal(part)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		for k, v := range fields {
			if _, dup := doc[k]; dup {
				return nil, fmt.Errorf("%w: relay %s: field %q is both common and %s's", ErrMalformed, r.Name, k, r.Proto)
			}
			doc[k] = v
		}
	}
	return json.Marshal(doc)
}

// UnmarshalJSON reads the common fields and the half Proto names.
func (r *RelayRecord) UnmarshalJSON(b []byte) error {
	var c Relay // the common fields
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	*r = RelayRecord{Name: c.Name, Address: c.Address, Port: c.Port, Proto: c.Proto, IssuedAt: c.IssuedAt, ExpiresAt: c.ExpiresAt}
	switch c.Proto {
	case ProtoVlessRealityVision:
		r.RealityParams = &RealityParams{}
		return json.Unmarshal(b, r.RealityParams)
	case ProtoHysteria2:
		r.HysteriaParams = &HysteriaParams{}
		return json.Unmarshal(b, r.HysteriaParams)
	case ProtoVlessXhttp:
		r.XhttpParams = &XhttpParams{}
		return json.Unmarshal(b, r.XhttpParams)
	}
	return fmt.Errorf("%w: relay %s proto %q", ErrMalformed, c.Name, c.Proto)
}

// halves: exactly the half Proto names is set.
func (r RelayRecord) halves() error {
	set := 0
	for _, h := range []bool{r.RealityParams != nil, r.HysteriaParams != nil, r.XhttpParams != nil} {
		if h {
			set++
		}
	}
	if p := r.params(); set != 1 || p == nil || reflect.ValueOf(p).IsNil() {
		return fmt.Errorf("%w: relay %s: %s needs exactly its own parameters", ErrMalformed, r.Name, r.Proto)
	}
	return nil
}

// RealityParams is the Reality half of a stream relay (record.rs Reality).
type RealityParams struct {
	PublicKey string   `json:"public_key"`
	UUID      string   `json:"uuid"`
	ShortID   string   `json:"short_id"`
	Covers    []string `json:"covers"`
}

// HysteriaParams is the Hysteria2 half of a voice relay (record.rs Hysteria).
type HysteriaParams struct {
	Region     string `json:"region"`
	Auth       string `json:"auth"`
	ServerName string `json:"server_name"`
	CertSHA256 string `json:"cert_sha256"`
	Masks      []Mask `json:"masks"`
}

// XhttpParams is the XHTTP half of a stream relay (record.rs Xhttp). Host is
// the relay's own domain — SNI and HTTP Host only, the client dials Address.
// Path is the secret the tunnel lives under, a credential like a Reality key.
type XhttpParams struct {
	Host string `json:"host"`
	Path string `json:"path"`
	UUID string `json:"uuid"`
	Mode string `json:"mode"`
}

// secretPathRe mirrors record.rs check_secret_path: "/" then non-empty
// segments of [A-Za-z0-9_-], 17..128 long in all.
var secretPathRe = regexp.MustCompile(`^(/[A-Za-z0-9_-]+)+$`)

// Mask is one UDP packet mask of a voice relay (record.rs Mask): type
// "salamander" with Password, or "noise" with Junk and ResetSecs.
type Mask struct {
	Type      string  `json:"type"`
	Password  string  `json:"password,omitempty"`
	Junk      []Junk  `json:"junk,omitempty"`
	ResetSecs *[2]int `json:"reset_secs,omitempty"`
}

// Junk is one junk packet of a noise mask: length in bytes and the pause
// after it in milliseconds, each an inclusive [from, to] range.
type Junk struct {
	Len     [2]int `json:"len"`
	DelayMs [2]int `json:"delay_ms"`
}

var hexPinRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SignRelay seals one relay record.
func (s *Signer) SignRelay(r RelayRecord) (Envelope, error) {
	if err := r.check(); err != nil {
		return Envelope{}, err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return Envelope{}, err
	}
	return s.seal(relayContext, payload), nil
}

// check is what the hub can get wrong while building a record. The full field
// rules live with the format (nekko-entry record.rs) and run in the client;
// this catches a hub bug at signing instead of in every client's log.
func (r RelayRecord) check() error {
	switch {
	case !nameRe.MatchString(r.Name):
		return fmt.Errorf("%w: name %q", ErrMalformed, r.Name)
	case r.Address == "" || r.Port <= 0 || r.Port > 65535:
		return fmt.Errorf("%w: relay %s has no address or port", ErrMalformed, r.Name)
	case r.IssuedAt <= 0 || r.ExpiresAt <= r.IssuedAt:
		return fmt.Errorf("%w: relay %s lifetime", ErrMalformed, r.Name)
	}
	if err := r.halves(); err != nil {
		return err
	}
	switch r.Proto {
	case ProtoVlessRealityVision:
		if len(r.RealityParams.Covers) == 0 {
			return fmt.Errorf("%w: relay %s has no covers", ErrMalformed, r.Name)
		}
	case ProtoVlessXhttp:
		x := r.XhttpParams
		switch {
		case x.Host == "" || net.ParseIP(x.Host) != nil || !strings.Contains(x.Host, "."):
			return fmt.Errorf("%w: relay %s: xhttp host must be a domain", ErrMalformed, r.Name)
		case len(x.Path) < 17 || len(x.Path) > 128 || !secretPathRe.MatchString(x.Path):
			return fmt.Errorf("%w: relay %s: xhttp secret path", ErrMalformed, r.Name)
		case x.UUID == "":
			return fmt.Errorf("%w: relay %s has no uuid", ErrMalformed, r.Name)
		case x.Mode != XhttpPacketUp && x.Mode != XhttpStreamUp && x.Mode != XhttpStreamOne:
			return fmt.Errorf("%w: relay %s: xhttp mode %q", ErrMalformed, r.Name, x.Mode)
		}
	case ProtoHysteria2:
		h := r.HysteriaParams
		switch {
		case !nameRe.MatchString(h.Region):
			return fmt.Errorf("%w: relay %s region %q", ErrMalformed, r.Name, h.Region)
		case len(h.Auth) < 16 || h.ServerName == "":
			return fmt.Errorf("%w: relay %s has no auth or server name", ErrMalformed, r.Name)
		case !hexPinRe.MatchString(h.CertSHA256):
			return fmt.Errorf("%w: relay %s certificate pin", ErrMalformed, r.Name)
		}
		for _, m := range h.Masks {
			switch {
			case m.Type == "salamander" && len(m.Password) >= 16 && m.Junk == nil && m.ResetSecs == nil:
			case m.Type == "noise" && m.Password == "" && len(m.Junk) > 0 && m.ResetSecs != nil:
			default:
				return fmt.Errorf("%w: relay %s mask %q", ErrMalformed, r.Name, m.Type)
			}
		}
	default:
		return fmt.Errorf("%w: relay %s proto %q", ErrMalformed, r.Name, r.Proto)
	}
	return nil
}

// Source is one place a client fetches records from without an account —
// nekko-entry record.rs Source: {"kind":"doh_txt","resolver","name"} or
// {"kind":"https","url"}.
type Source struct {
	Kind     string `json:"kind"`
	Resolver string `json:"resolver,omitempty"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url,omitempty"`
}

// SourceList is the signed payload of a source list (record.rs SourceList).
type SourceList struct {
	Sources   []Source `json:"sources"`
	IssuedAt  int64    `json:"issued_at"`
	ExpiresAt int64    `json:"expires_at"`
}

// SignSources seals a source list. Shape is checked here so the operator hears
// about a typo at once rather than from clients silently dropping the list.
func (s *Signer) SignSources(l SourceList) (Envelope, error) {
	if err := l.check(); err != nil {
		return Envelope{}, err
	}
	payload, err := json.Marshal(l)
	if err != nil {
		return Envelope{}, err
	}
	return s.seal(sourcesContext, payload), nil
}

func (l SourceList) check() error {
	if len(l.Sources) == 0 || len(l.Sources) > 64 {
		return fmt.Errorf("%w: sources must hold 1..64 entries", ErrMalformed)
	}
	seen := map[Source]bool{}
	for _, src := range l.Sources {
		switch src.Kind {
		case "doh_txt":
			if !strings.HasPrefix(src.Resolver, "https://") || src.Name == "" || src.URL != "" {
				return fmt.Errorf("%w: doh_txt needs an https resolver and a name", ErrMalformed)
			}
		case "https":
			if !strings.HasPrefix(src.URL, "https://") || src.Resolver != "" || src.Name != "" {
				return fmt.Errorf("%w: https needs an https url", ErrMalformed)
			}
		default:
			return fmt.Errorf("%w: source kind %q", ErrMalformed, src.Kind)
		}
		if seen[src] {
			return fmt.Errorf("%w: duplicate source", ErrMalformed)
		}
		seen[src] = true
	}
	if l.IssuedAt <= 0 || l.ExpiresAt <= l.IssuedAt {
		return fmt.Errorf("%w: source list lifetime", ErrMalformed)
	}
	return nil
}

// seal signs context‖payload — the layout openEnvelope and nekko-entry
// envelope.rs verify.
func (s *Signer) seal(context string, payload []byte) Envelope {
	msg := make([]byte, 0, len(context)+len(payload))
	msg = append(append(msg, context...), payload...)
	return Envelope{
		KeyID:     s.id,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(s.key, msg)),
		Payload:   base64.StdEncoding.EncodeToString(payload),
	}
}
