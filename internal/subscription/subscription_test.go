package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

const testUUID = "00000000-0000-4000-8000-000000000001"

func realityRecord() entry.RelayRecord {
	return entry.RelayRecord{
		Name: "ams-1", Address: "203.0.113.7", Port: 443, Proto: entry.ProtoVlessRealityVision,
		RealityParams: &entry.RealityParams{
			PublicKey: "iQ6pQ8v1d6s1j8O1oV3mY8xJ2u0yJb4h9x2bS3q5wFk", UUID: testUUID,
			ShortID: "0123456789abcdef", Covers: []string{"www.cover.example", "www.other.example"},
		},
	}
}

func xhttpRecord() entry.RelayRecord {
	return entry.RelayRecord{
		Name: "ams-2", Address: "203.0.113.8", Port: 443, Proto: entry.ProtoVlessXhttp,
		XhttpParams: &entry.XhttpParams{Host: "notes.example-relay.xyz", Path: "/k7Qp2_Zx9-mWv3Lr", UUID: testUUID, Mode: entry.XhttpPacketUp},
	}
}

func TestARealityRelayBecomesAVlessRealityLink(t *testing.T) {
	link, err := Link(realityRecord())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "vless" || u.User.Username() != testUUID || u.Host != "203.0.113.7:443" || u.Fragment != "Hikkari ams-1" {
		t.Fatalf("link %s", link)
	}
	for k, want := range map[string]string{
		"security": "reality", "flow": "xtls-rprx-vision", "type": "tcp", "sni": "www.cover.example",
		"pbk": realityRecord().RealityParams.PublicKey, "sid": "0123456789abcdef", "encryption": "none",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestAnXhttpRelayBecomesAVlessXhttpLink(t *testing.T) {
	link, err := Link(xhttpRecord())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	for k, want := range map[string]string{
		"security": "tls", "type": "xhttp", "sni": "notes.example-relay.xyz", "host": "notes.example-relay.xyz",
		"path": "/k7Qp2_Zx9-mWv3Lr", "mode": "packet-up",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestAHysteriaRelayHasNoLinkYet(t *testing.T) {
	r := entry.RelayRecord{Name: "v-1", Address: "203.0.113.9", Port: 443, Proto: entry.ProtoHysteria2,
		HysteriaParams: &entry.HysteriaParams{Auth: "secret"}}
	if _, err := Link(r); err == nil {
		t.Fatal("a hysteria2 relay was rendered without its pin and masks")
	}
}

func hysteriaRecord(spki string) entry.RelayRecord {
	return entry.RelayRecord{Name: "eu-1", Address: "203.0.113.10", Port: 443, Proto: entry.ProtoHysteria2,
		HysteriaParams: &entry.HysteriaParams{
			Region: "eu", Auth: "0123456789abcdef0123", ServerName: "mvtrgrvss.io",
			CertSHA256: strings.Repeat("ab", 32), PubKeySHA256: spki,
			Masks: []entry.Mask{{Type: "salamander", Password: "fedcba9876543210"}},
		}}
}

// A client without Xray gets Hysteria2 as plain salamander, pinned by the
// leaf's public key — never Xray's finalmask or certificate pin, which
// sing-box refuses, and never a link with no pin at all.
func TestAHysteriaRelayForSingBoxIsSalamanderPinnedByItsKey(t *testing.T) {
	spki := strings.Repeat("cd", 32)
	link, err := SingBoxHysteriaLink(hysteriaRecord(spki))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	if u.Scheme != "hysteria2" || u.User.Username() != "0123456789abcdef0123" || u.Host != "203.0.113.10:443" {
		t.Fatalf("link %s", link)
	}
	for k, want := range map[string]string{
		"sni": "mvtrgrvss.io", "alpn": "h3", "obfs": "salamander", "obfs-password": "fedcba9876543210",
		"hikkari-spki": spki, "pinSHA256": "", "finalmask": "", "insecure": "",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if _, err := SingBoxHysteriaLink(hysteriaRecord("")); err == nil {
		t.Fatal("a record without the public key pin was rendered unpinned")
	}
}

// fakeBatch serves whatever records it was given, signed, to any account.
type fakeBatch struct {
	envelopes [][]byte
	asked     []uuid.UUID
}

func (f *fakeBatch) BundleFor(_ context.Context, account uuid.UUID, _ entry.GrantMeta, _ string) (entry.Bundle, error) {
	f.asked = append(f.asked, account)
	return entry.Bundle{Relays: f.envelopes}, nil
}

func signed(t *testing.T, s *entry.Signer, r entry.RelayRecord) []byte {
	t.Helper()
	now := time.Now()
	r.IssuedAt, r.ExpiresAt = now.Unix(), now.Add(time.Hour).Unix()
	env, err := s.SignRelay(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func livePool(t *testing.T) db.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run subscription integration tests")
	}
	ctx := context.Background()
	p, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := db.RunMigrations(ctx, p); err != nil {
		t.Fatal(err)
	}
	cryptobox.ConfigureEphemeralForTests()
	return p
}

// A subscriber gets a token once; GET /sub/<token> serves their batch as
// base64 links, a wrong token is a 404, and a revoked one stops at once.
func TestASubscriptionServesTheBatchAndStopsWhenRevoked(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	batch := &fakeBatch{envelopes: [][]byte{signed(t, signer, realityRecord()), signed(t, signer, xhttpRecord())}}
	s := New(pool, batch, []entry.Key{signer.Key()})
	ctx := context.Background()
	name := "t-" + uuid.NewString()[:8]
	token, err := s.Add(ctx, name, unlimited(t, s))
	if err != nil {
		t.Fatal(err)
	}

	get := func(tok string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /sub/{token}", s.Handler())
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/sub/"+tok, nil))
		return rec
	}
	rec := get(token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	links := strings.Split(string(body), "\n")
	if len(links) != 2 || !strings.HasPrefix(links[0], "vless://") || !strings.Contains(links[1], "type=xhttp") {
		t.Fatalf("links %q", links)
	}
	if len(batch.asked) != 1 || batch.asked[0] == uuid.Nil {
		t.Fatalf("batch asked for %v", batch.asked)
	}

	if rec := get("x" + token[1:]); rec.Code != http.StatusNotFound {
		t.Fatalf("a wrong token: %d", rec.Code)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hub_subscriptions WHERE token_hash = convert_to($1, 'UTF8')`, token).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the token itself is stored: %d %v", stored, err)
	}
	if err := s.Revoke(ctx, name); err != nil {
		t.Fatal(err)
	}
	if rec := get(token); rec.Code != http.StatusNotFound {
		t.Fatalf("a revoked token: %d", rec.Code)
	}
	if err := s.Revoke(ctx, name); err != ErrUnknown {
		t.Fatalf("second revoke: %v", err)
	}
}

// A record signed by someone else is not served.
func TestARecordUnderAnUnknownKeyIsLeftOut(t *testing.T) {
	pool := livePool(t)
	ours := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	theirs := entry.NewSigner("entry-1", bytes.Repeat([]byte{4}, 32))
	batch := &fakeBatch{envelopes: [][]byte{signed(t, theirs, realityRecord()), signed(t, ours, xhttpRecord())}}
	s := New(pool, batch, []entry.Key{ours.Key()})
	ctx := context.Background()
	token, err := s.Add(ctx, "t-"+uuid.NewString()[:8], unlimited(t, s))
	if err != nil {
		t.Fatal(err)
	}
	links, err := s.Links(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || !strings.Contains(links[0], "type=xhttp") {
		t.Fatalf("links %q", links)
	}
}
