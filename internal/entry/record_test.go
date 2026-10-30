package entry

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// fixture is testdata/relay.fixture.json, written by the Rust side
// (nekko-entry envelope.rs, the_cross_language_fixture_still_verifies). Both
// sides verify this one file, so a format or context drift on either side
// fails that side's tests.
type fixture struct {
	SeedHex         string          `json:"seed_hex"`
	PublicKeyHex    string          `json:"public_key_hex"`
	KeyID           string          `json:"key_id"`
	Now             int64           `json:"now"`
	Relay           json.RawMessage `json:"relay"`
	Envelope        Envelope        `json:"envelope"`
	SourcesEnvelope Envelope        `json:"sources_envelope"`
}

func loadFixture(t testing.TB) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/relay.fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) keys(t testing.TB) []Key {
	k, err := ParseKey(f.KeyID, f.PublicKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	return []Key{k}
}

// signer is the fixture's test seed — not a real key.
func (f fixture) signer(t testing.TB) ed25519.PrivateKey {
	seed, err := hex.DecodeString(f.SeedHex)
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func sealWith(priv ed25519.PrivateKey, context string, payload []byte) Envelope {
	return Envelope{
		KeyID:     "entry-1",
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, append([]byte(context), payload...))),
		Payload:   base64.StdEncoding.EncodeToString(payload),
	}
}

func TestTheHubAgreesWithNekkoEntryOnARealRecord(t *testing.T) {
	f := loadFixture(t)
	r, err := Verify(f.Envelope, f.keys(t), time.Unix(f.Now, 0))
	if err != nil {
		t.Fatalf("a record nekko-entry signed: %v", err)
	}
	var want Relay
	if err := json.Unmarshal(f.Relay, &want); err != nil {
		t.Fatal(err)
	}
	if r != want {
		t.Fatalf("got %+v, want %+v", r, want)
	}
}

func TestASourceListIsNotARelay(t *testing.T) {
	// Genuine, same key — but signed as the other kind.
	f := loadFixture(t)
	if _, err := Verify(f.SourcesEnvelope, f.keys(t), time.Unix(f.Now, 0)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("sources envelope as a relay: %v, want ErrBadSignature", err)
	}
}

func TestTheHubAgreesWithNekkoEntryOnASourceList(t *testing.T) {
	f := loadFixture(t)
	m, err := VerifySources(f.SourcesEnvelope, f.keys(t), time.Unix(f.Now, 0))
	if err != nil {
		t.Fatalf("a source list nekko-entry signed: %v", err)
	}
	if m.IssuedAt <= 0 || m.ExpiresAt <= m.IssuedAt {
		t.Fatalf("source list meta: %+v", m)
	}
	// Past its expiry the same genuine list is refused — the hub never serves
	// one the client would reject.
	if _, err := VerifySources(f.SourcesEnvelope, f.keys(t), time.Unix(m.ExpiresAt, 0)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired source list: %v, want ErrExpired", err)
	}
}

func TestARelayIsNotASourceList(t *testing.T) {
	// Genuine, same key — but signed as the other kind: the sources context
	// does not verify a relay signature.
	f := loadFixture(t)
	if _, err := VerifySources(f.Envelope, f.keys(t), time.Unix(f.Now, 0)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("relay envelope as a source list: %v, want ErrBadSignature", err)
	}
}

func TestAnAlteredRecordIsRefused(t *testing.T) {
	f := loadFixture(t)
	payload, _ := base64.StdEncoding.DecodeString(f.Envelope.Payload)
	env := f.Envelope
	env.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), "203.0.113.7", "203.0.113.8", 1)))
	if _, err := Verify(env, f.keys(t), time.Unix(f.Now, 0)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("altered address: %v, want ErrBadSignature", err)
	}
}

func TestAnotherKeyUnderOurIDIsRefused(t *testing.T) {
	f := loadFixture(t)
	_, stranger, _ := ed25519.GenerateKey(nil)
	payload, _ := base64.StdEncoding.DecodeString(f.Envelope.Payload)
	env := sealWith(stranger, relayContext, payload)
	if _, err := Verify(env, f.keys(t), time.Unix(f.Now, 0)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("stranger's signature: %v, want ErrBadSignature", err)
	}
	env.KeyID = "entry-9"
	if _, err := Verify(env, f.keys(t), time.Unix(f.Now, 0)); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key id: %v, want ErrUnknownKey", err)
	}
}

func TestAnExpiredRecordIsRefused(t *testing.T) {
	f := loadFixture(t)
	var r Relay
	_ = json.Unmarshal(f.Relay, &r)
	if _, err := Verify(f.Envelope, f.keys(t), time.Unix(r.ExpiresAt, 0)); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expires_at: %v, want ErrExpired", err)
	}
}

func TestASignedButMalformedRecordIsRefused(t *testing.T) {
	f := loadFixture(t)
	priv := f.signer(t)
	for _, payload := range []string{
		`{}`,
		`not json`,
		`{"name":"UPPER","address":"1.2.3.4","port":443,"proto":"p","issued_at":1,"expires_at":9999999999}`,
		`{"name":"a","address":"","port":443,"proto":"p","issued_at":1,"expires_at":9999999999}`,
		`{"name":"a","address":"1.2.3.4","port":0,"proto":"p","issued_at":1,"expires_at":9999999999}`,
		`{"name":"a","address":"1.2.3.4","port":443,"proto":"p","issued_at":5,"expires_at":5}`,
	} {
		env := sealWith(priv, relayContext, []byte(payload))
		if _, err := Verify(env, f.keys(t), time.Unix(f.Now, 0)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: %v, want ErrMalformed", payload, err)
		}
	}
}
