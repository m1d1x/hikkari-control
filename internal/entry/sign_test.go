package entry

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/config"
)

// hubFixture is testdata/hub-relay.fixture.json: records the HUB signed, for
// the client to verify (nekko-entry envelope.rs,
// the_hub_signed_fixture_verifies). The mirror of relay.fixture.json, which
// the Rust side writes for the hub. Regenerate with
// NEKKO_ENTRY_REGEN_FIXTURE=1 go test ./internal/entry -run HubFixture.
type hubFixture struct {
	PublicKeyHex    string      `json:"public_key_hex"`
	KeyID           string      `json:"key_id"`
	Now             int64       `json:"now"`
	Relay           RelayRecord `json:"relay"`
	VoiceRelay      RelayRecord `json:"voice_relay"`
	XhttpRelay      RelayRecord `json:"xhttp_relay"`
	Sources         SourceList  `json:"sources"`
	Envelope        Envelope    `json:"envelope"`
	VoiceEnvelope   Envelope    `json:"voice_envelope"`
	XhttpEnvelope   Envelope    `json:"xhttp_envelope"`
	SourcesEnvelope Envelope    `json:"sources_envelope"`
}

const hubFixturePath = "testdata/hub-relay.fixture.json"

func testRecord() RelayRecord {
	return RelayRecord{
		Name:    "ams1-1",
		Address: "203.0.113.7",
		Port:    443,
		Proto:   ProtoVlessRealityVision,
		RealityParams: &RealityParams{
			PublicKey: "BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc",
			UUID:      "00000000-1111-2222-3333-444444444444",
			ShortID:   "0123abcd",
			Covers:    []string{"www.samsung.com", "www.nvidia.com"},
		},
		IssuedAt:  1_760_000_000,
		ExpiresAt: 1_762_592_000,
	}
}

func testVoiceRecord() RelayRecord {
	return RelayRecord{
		Name:    "ams1-2",
		Address: "203.0.113.8",
		Port:    8443,
		Proto:   ProtoHysteria2,
		HysteriaParams: &HysteriaParams{
			Region:     "ru",
			Auth:       "aaaaaaaaaaaaaaaaaaaa",
			ServerName: "ams1-2.relay",
			CertSHA256: "abababababababababababababababababababababababababababababababab",
			Masks: []Mask{
				{Type: "noise", Junk: []Junk{{Len: [2]int{40, 90}, DelayMs: [2]int{1, 3}}}, ResetSecs: &[2]int{0, 0}},
				{Type: "salamander", Password: "pwpwpwpwpwpwpwpwpwpw"},
			},
		},
		IssuedAt:  1_760_000_000,
		ExpiresAt: 1_762_592_000,
	}
}

func testXhttpRecord() RelayRecord {
	return RelayRecord{
		Name:    "ams1-3",
		Address: "203.0.113.9",
		Port:    443,
		Proto:   ProtoVlessXhttp,
		XhttpParams: &XhttpParams{
			Host: "cdn.example-relay.xyz",
			Path: "/s3cr3tPathAbCdEf",
			UUID: "55555555-6666-7777-8888-999999999999",
			Mode: XhttpStreamUp,
		},
		IssuedAt:  1_760_000_000,
		ExpiresAt: 1_762_592_000,
	}
}

func testSources() SourceList {
	return SourceList{
		Sources: []Source{
			{Kind: "doh_txt", Resolver: "https://dns.example.org/dns-query", Name: "e.example.org"},
			{Kind: "https", URL: "https://cdn.example.org/e.json"},
		},
		IssuedAt:  1_760_000_000,
		ExpiresAt: 1_762_592_000,
	}
}

func TestHubFixture(t *testing.T) {
	f := loadFixture(t) // the same test seed the Rust fixture uses
	seed, err := hex.DecodeString(f.SeedHex)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSigner(f.KeyID, seed)
	if os.Getenv("NEKKO_ENTRY_REGEN_FIXTURE") != "" {
		env, err := s.SignRelay(testRecord())
		if err != nil {
			t.Fatal(err)
		}
		venv, err := s.SignRelay(testVoiceRecord())
		if err != nil {
			t.Fatal(err)
		}
		xenv, err := s.SignRelay(testXhttpRecord())
		if err != nil {
			t.Fatal(err)
		}
		senv, err := s.SignSources(testSources())
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.MarshalIndent(hubFixture{
			PublicKeyHex: f.PublicKeyHex, KeyID: f.KeyID, Now: f.Now,
			Relay: testRecord(), VoiceRelay: testVoiceRecord(), XhttpRelay: testXhttpRecord(),
			Sources:  testSources(),
			Envelope: env, VoiceEnvelope: venv, XhttpEnvelope: xenv, SourcesEnvelope: senv,
		}, "", "  ")
		if err := os.WriteFile(hubFixturePath, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(hubFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var h hubFixture
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	// What the hub signs, the hub's own verify accepts — and the Rust side
	// verifies this same file.
	r, err := Verify(h.Envelope, f.keys(t), time.Unix(h.Now, 0))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "ams1-1" || r.Port != 443 || r.Proto != ProtoVlessRealityVision {
		t.Fatalf("verified %+v", r)
	}
	v, err := Verify(h.VoiceEnvelope, f.keys(t), time.Unix(h.Now, 0))
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "ams1-2" || v.Port != 8443 || v.Proto != ProtoHysteria2 {
		t.Fatalf("verified %+v", v)
	}
	x, err := Verify(h.XhttpEnvelope, f.keys(t), time.Unix(h.Now, 0))
	if err != nil {
		t.Fatal(err)
	}
	if x.Name != "ams1-3" || x.Proto != ProtoVlessXhttp {
		t.Fatalf("verified %+v", x)
	}
	// The uuid both Reality and XHTTP name survives the flattening.
	if got := h.XhttpRelay.XhttpParams; got == nil || *got != *testXhttpRecord().XhttpParams {
		t.Fatalf("xhttp half read back as %+v", got)
	}
	if _, err := VerifySources(h.SourcesEnvelope, f.keys(t), time.Unix(h.Now, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSigner(t *testing.T) {
	f := loadFixture(t)
	env := func(v string) func(string) string { return func(string) string { return v } }

	s, err := LoadSigner(env(""), f.keys(t))
	if s != nil || err != nil {
		t.Fatalf("unset: %v %v, want nil nil", s, err)
	}
	s, err = LoadSigner(env(f.SeedHex), f.keys(t))
	if err != nil || s == nil || s.id != f.KeyID {
		t.Fatalf("pinned seed: %v %v", s, err)
	}
	// A key no client pins must not start signing.
	other := "1111111111111111111111111111111111111111111111111111111111111111"
	if _, err := LoadSigner(env(other), f.keys(t)); err == nil {
		t.Fatal("an unpinned key was accepted")
	}
	if _, err := LoadSigner(env("zz"), f.keys(t)); err == nil {
		t.Fatal("garbage was accepted")
	}
}

func TestSignRefusesBrokenRecords(t *testing.T) {
	s := NewSigner("entry-1", make([]byte, 32))
	r := testRecord()
	r.RealityParams.Covers = nil
	if _, err := s.SignRelay(r); !errors.Is(err, ErrMalformed) {
		t.Fatalf("no covers: %v", err)
	}
	for what, edit := range map[string]func(*RelayRecord){
		"reality params on a voice relay": func(r *RelayRecord) { r.RealityParams = testRecord().RealityParams },
		"no voice params":                 func(r *RelayRecord) { r.HysteriaParams = nil },
		"short pin":                       func(r *RelayRecord) { r.HysteriaParams.CertSHA256 = "ab" },
		"no region":                       func(r *RelayRecord) { r.HysteriaParams.Region = "" },
		"unknown mask":                    func(r *RelayRecord) { r.HysteriaParams.Masks = []Mask{{Type: "prefix"}} },
		"noise without junk":              func(r *RelayRecord) { r.HysteriaParams.Masks = []Mask{{Type: "noise", ResetSecs: &[2]int{0, 0}}} },
		"unknown proto":                   func(r *RelayRecord) { r.Proto = "wireguard" },
	} {
		v := testVoiceRecord()
		edit(&v)
		if _, err := s.SignRelay(v); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	for what, edit := range map[string]func(*XhttpParams){
		"ip as host":    func(x *XhttpParams) { x.Host = "203.0.113.9" },
		"bare label":    func(x *XhttpParams) { x.Host = "localhost" },
		"short path":    func(x *XhttpParams) { x.Path = "/abc" },
		"empty segment": func(x *XhttpParams) { x.Path = "/s3cr3tPath//AbCdEf" },
		"dot segment":   func(x *XhttpParams) { x.Path = "/s3cr3tPathAbCd/../x" },
		"no uuid":       func(x *XhttpParams) { x.UUID = "" },
		"auto mode":     func(x *XhttpParams) { x.Mode = "auto" },
	} {
		x := testXhttpRecord()
		edit(x.XhttpParams)
		if _, err := s.SignRelay(x); !errors.Is(err, ErrMalformed) {
			t.Fatalf("xhttp %s: %v", what, err)
		}
	}
	x := testXhttpRecord()
	x.RealityParams = testRecord().RealityParams
	if _, err := s.SignRelay(x); !errors.Is(err, ErrMalformed) {
		t.Fatalf("xhttp with reality params: %v", err)
	}
	l := testSources()
	l.Sources = append(l.Sources, l.Sources[0])
	if _, err := s.SignSources(l); !errors.Is(err, ErrMalformed) {
		t.Fatalf("duplicate source: %v", err)
	}
	l = testSources()
	l.Sources[1].URL = "http://plain.example.org"
	if _, err := s.SignSources(l); !errors.Is(err, ErrMalformed) {
		t.Fatalf("plain http: %v", err)
	}
}

func TestNewIdentityIsDialable(t *testing.T) {
	id, err := newIdentity(443, 10001)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSigner("entry-1", make([]byte, 32))
	env, err := s.SignRelay(id.record("ams1-1", "203.0.113.7", "", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	keys := []Key{{ID: "entry-1", Key: s.key.Public().(ed25519.PublicKey)}}
	if _, err := Verify(env, keys, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	e, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	if e["EDGE_LISTEN"] != "0.0.0.0:443" || e["EDGE_CLIENTS"] != id.UUID || e["EDGE_UPSTREAMS"] != "" ||
		e["EDGE_ECHO_PORT"] != "10100" || e["EDGE_MEDIA"] != "" {
		t.Fatalf("edge env %v", e)
	}
}

// An XHTTP relay the hub mints signs into a record with the box's domain as
// host and a fresh secret path, and renders an env that names no destination.
func TestNewXhttpIdentityIsDialable(t *testing.T) {
	id, err := newXhttpIdentity(443, 10001, "notes.example-relay.xyz")
	if err != nil {
		t.Fatal(err)
	}
	x := id.Xhttp
	if x == nil || id.proto() != ProtoVlessXhttp || id.echoPort() != 0 {
		t.Fatalf("identity %+v", id)
	}
	if len(x.Path) != 25 || !secretPathRe.MatchString(x.Path) || x.Mode != XhttpStreamUp {
		t.Fatalf("path %q mode %q", x.Path, x.Mode)
	}
	other, _ := newXhttpIdentity(443, 10001, "notes.example-relay.xyz")
	if other.Xhttp.Path == x.Path || other.UUID == id.UUID {
		t.Fatal("two relays drew the same secret")
	}
	s := NewSigner("entry-1", make([]byte, 32))
	env, err := s.SignRelay(id.record("ams1-1", "203.0.113.7", "", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	keys := []Key{{ID: "entry-1", Key: s.key.Public().(ed25519.PublicKey)}}
	if _, err := Verify(env, keys, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	var r RelayRecord
	if err := json.Unmarshal(payload, &r); err != nil {
		t.Fatal(err)
	}
	if r.Address != "203.0.113.7" || r.XhttpParams == nil || r.XhttpParams.Host != "notes.example-relay.xyz" ||
		r.XhttpParams.UUID != id.UUID || r.XhttpParams.Path != x.Path {
		t.Fatalf("record %+v %+v", r, r.XhttpParams)
	}
	e, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	if e["EDGE_PROTO"] != ProtoVlessXhttp || e["EDGE_XH_DOMAIN"] != "notes.example-relay.xyz" ||
		e["EDGE_XH_PATH"] != x.Path || e["EDGE_CLIENTS"] != id.UUID || e["EDGE_UPSTREAMS"] != "" ||
		e["EDGE_MEDIA"] != "" || e["EDGE_ECHO_PORT"] != "" {
		t.Fatalf("edge env %v", e)
	}
}

func TestARelayDomainIsAPlainDomainAndNeverTheBrands(t *testing.T) {
	for _, ok := range []string{"notes.example-relay.xyz", "a-b.c.top"} {
		if err := checkRelayDomain(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	base := config.Default().Base
	for _, bad := range []string{"", "203.0.113.7", "localhost", "Upper.xyz", "a..b.xyz", "-a.xyz", "x.y/z", base, "relay." + base} {
		if err := checkRelayDomain(bad); !errors.Is(err, ErrRejected) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// A voice relay the hub mints signs into a record the client's rules accept
// (nekko-entry record.rs Hysteria::validate, mirrored where it can be), pins
// exactly the certificate the edge serves, and renders an env that forwards
// to the given media and nowhere else.
func TestNewVoiceIdentityIsDialable(t *testing.T) {
	id, err := newVoiceIdentity(443, 10001)
	if err != nil {
		t.Fatal(err)
	}
	h := id.Hysteria
	if h == nil || id.proto() != ProtoHysteria2 {
		t.Fatalf("not a voice identity: %+v", id)
	}
	block, _ := pem.Decode([]byte(h.CertPEM))
	if block == nil {
		t.Fatal("certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(block.Bytes); hex.EncodeToString(sum[:]) != h.CertSHA256 {
		t.Fatal("pin is not the certificate's")
	}
	if err := cert.VerifyHostname(h.ServerName); err != nil {
		t.Fatalf("certificate does not name %s: %v", h.ServerName, err)
	}
	if len(h.Auth) < 16 || len(h.Masks) != 2 || h.Masks[0].Type != "salamander" || len(h.Masks[0].Password) < 16 {
		t.Fatalf("credentials: %+v", h)
	}
	for _, j := range h.Masks[1].Junk {
		if j.Len[0] < 1 || j.Len[0] > j.Len[1] || j.Len[1] > 1200 || j.DelayMs[1] > 20 {
			t.Fatalf("junk outside what a client accepts: %+v", j)
		}
	}

	s := NewSigner("entry-1", make([]byte, 32))
	env, err := s.SignRelay(id.record("ams2-1", "203.0.113.8", "ru", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	keys := []Key{{ID: "entry-1", Key: s.key.Public().(ed25519.PublicKey)}}
	r, err := Verify(env, keys, time.Unix(1, 0))
	if err != nil || r.Proto != ProtoHysteria2 {
		t.Fatalf("verify: %+v %v", r, err)
	}

	e, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	if e["EDGE_PROTO"] != ProtoHysteria2 || e["EDGE_MEDIA"] != "" ||
		e["EDGE_LISTEN"] != "0.0.0.0:443" || e["EDGE_ECHO_PORT"] != "10001" || e["EDGE_UPSTREAMS"] != "" {
		t.Fatalf("voice env %v", e)
	}
	for _, k := range []string{"EDGE_HY_CERT", "EDGE_HY_KEY", "EDGE_HY_FINALMASK"} {
		raw, err := base64.StdEncoding.DecodeString(e[k])
		if err != nil || len(raw) == 0 {
			t.Fatalf("%s is not base64: %v", k, err)
		}
	}
}

// The finalmask the hub renders is the one edge/voice_test.go dials through
// (testFinalmask there): pinned here so the two cannot drift apart unseen.
func TestFinalmaskRendersAsTheEdgeExpects(t *testing.T) {
	got, err := json.Marshal(xrayFinalmask([]Mask{
		{Type: "salamander", Password: "pwpwpwpwpwpwpwpwpwpw"},
		{Type: "noise", Junk: []Junk{{Len: [2]int{40, 90}, DelayMs: [2]int{0, 2}}}, ResetSecs: &[2]int{0, 0}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"settings":{"password":"pwpwpwpwpwpwpwpwpwpw"},"type":"salamander"},` +
		`{"settings":{"noise":[{"delay":"0-2","rand":"40-90"}],"reset":"0-0"},"type":"noise"}]`
	if string(got) != want {
		t.Fatalf("finalmask\n got %s\nwant %s", got, want)
	}
}
