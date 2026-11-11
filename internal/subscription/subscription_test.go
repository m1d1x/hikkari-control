package subscription

import (
	"errors"
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

	// One character off, never the token itself: a random token may start
	// with the replacement.
	wrong := "x" + token[1:]
	if token[0] == 'x' {
		wrong = "y" + token[1:]
	}
	if rec := get(wrong); rec.Code != http.StatusNotFound {
		t.Fatalf("a wrong token: %d", rec.Code)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hub_subscriptions WHERE token_hash = convert_to($1, 'UTF8')`, token).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the token itself is stored: %d %v", stored, err)
	}
	if err := s.Revoke(ctx, name); err != nil {
		t.Fatal(err)
	}
	// Revoked for good: a 410 our client reads as the end and deletes the
	// subscription; a token never issued stays a 404.
	if rec := get(token); rec.Code != http.StatusGone || rec.Header().Get(revokedHeader) != "revoked" {
		t.Fatalf("a revoked token: %d %q", rec.Code, rec.Header().Get(revokedHeader))
	}
	if rec := get(wrong); rec.Code != http.StatusNotFound {
		t.Fatalf("a wrong token after the revoke: %d", rec.Code)
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

func TestALinkTakesItsNodesFingerprint(t *testing.T) {
	link, err := Link(realityRecord())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(namedLink(link, entry.NodeName{Slug: "abc", Fingerprint: "firefox"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("fp"); got != "firefox" {
		t.Fatalf("fp %q in %s", got, u)
	}
	u, err = url.Parse(namedLink(link, entry.NodeName{Slug: "abc"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("fp"); got != "chrome" {
		t.Fatalf("a node with no fingerprint changed fp to %q", got)
	}
	plain := "hysteria2://auth@1.2.3.4:443?sni=x#n"
	u, err = url.Parse(namedLink(plain, entry.NodeName{Slug: "abc", Fingerprint: "firefox"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Has("fp") {
		t.Fatalf("a link with no fingerprint got one: %s", u)
	}
}

func TestALinkCarriesItsNodesNameAndID(t *testing.T) {
	link, err := Link(realityRecord())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(namedLink(link, entry.NodeName{Slug: "abc", Label: "рига-001"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if u.Fragment != "рига-001" || u.Query().Get("hikkari-node") != "n:abc" {
		t.Fatalf("link %s", u)
	}
	if got := namedLink(link, entry.NodeName{}, nil); got != link {
		t.Fatalf("a relay with no node changed: %s", got)
	}
}

func TestALinkTakesItsNodesSNIOnlyFromItsCovers(t *testing.T) {
	rec := realityRecord()
	rec.RealityParams.Covers = []string{"www.samsung.com", "www.lg.com"}
	link, err := Link(rec)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(namedLink(link, entry.NodeName{Slug: "abc", SNI: "www.lg.com"}, coversOf(rec)))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("sni"); got != "www.lg.com" {
		t.Fatalf("sni %q in %s", got, u)
	}
	// A name the relay does not serve would make the link dead: the first
	// cover stays.
	u, err = url.Parse(namedLink(link, entry.NodeName{Slug: "abc", SNI: "www.apple.com"}, coversOf(rec)))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("sni"); got != "www.samsung.com" {
		t.Fatalf("sni %q for a cover the relay lacks", got)
	}
	xhttp := "vless://u@1.2.3.4:443?security=tls&type=xhttp&sni=own.example&host=own.example&alpn=h2%2Chttp%2F1.1&fp=chrome#n"
	u, err = url.Parse(namedLink(xhttp, entry.NodeName{Slug: "abc", SNI: "www.lg.com"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("sni"); got != "own.example" {
		t.Fatalf("xhttp sni changed to %q", got)
	}
}

func TestALinkTakesItsNodesALPN(t *testing.T) {
	link, err := Link(realityRecord())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(namedLink(link, entry.NodeName{Slug: "abc", ALPN: "http/1.1"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("alpn"); got != "http/1.1" {
		t.Fatalf("alpn %q in %s", got, u)
	}
	u, err = url.Parse(namedLink(link, entry.NodeName{Slug: "abc"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Has("alpn") {
		t.Fatalf("a node with no alpn got one: %s", u)
	}
	plain := "hysteria2://auth@1.2.3.4:443?sni=x&alpn=h3#n"
	u, err = url.Parse(namedLink(plain, entry.NodeName{Slug: "abc", ALPN: "h2"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("alpn"); got != "h3" {
		t.Fatalf("hysteria alpn changed to %q", got)
	}
}

func TestTheClientNamesItsNetwork(t *testing.T) {
	for v, want := range map[string]uint32{
		"AS12389": 12389, " AS8359 ": 8359, "": 0, "12389": 0, "AS": 0, "AS0": 0, "AS012": 0,
		"AS4294967296": 0, "AS-1": 0, "as12389": 0, "AS12389x": 0,
	} {
		h := http.Header{}
		if v != "" {
			h.Set(networkHeader, v)
		}
		if got := networkOf(h); got != want {
			t.Errorf("%q: got %d, want %d", v, got, want)
		}
	}
}

// A subscription issued by hand carries its title to the client, and a
// tester's says so inside the sealed body only, where nobody on the way can
// set it.
func TestAnIssuedSubscriptionCarriesItsTitleAndTesterFlag(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	s := New(pool, &fakeBatch{envelopes: [][]byte{signed(t, signer, realityRecord())}}, []entry.Key{signer.Key()})
	ctx := context.Background()
	fetch := func(token string) (*httptest.ResponseRecorder, string) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /sub/{token}", s.Handler())
		req := httptest.NewRequest("GET", "/sub/"+token, nil)
		req.Header.Set("Hikkari-Features", "relay-metrics-v2")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		body, err := openSubscription(token, rec.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return rec, string(body)
	}
	title := func(rec *httptest.ResponseRecorder) string {
		raw, _ := strings.CutPrefix(rec.Header().Get("Profile-Title"), "base64:")
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	name := "t-" + uuid.NewString()[:8]
	token, err := s.Issue(ctx, Issue{Name: name, Plan: unlimited(t, s), Title: "  Петя, тест  ", Tester: true})
	if err != nil {
		t.Fatal(err)
	}
	rec, body := fetch(token)
	if got := title(rec); got != "Петя, тест" {
		t.Fatalf("title %q", got)
	}
	if !strings.Contains(body, `"tester":true`) {
		t.Fatalf("a tester's sealed body says nothing: %s", body)
	}
	if err := s.SetTitle(ctx, name, ""); err != nil {
		t.Fatal(err)
	}
	if rec, _ := fetch(token); title(rec) != "hikkari" {
		t.Fatalf("cleared title %q", title(rec))
	}

	plain, err := s.Issue(ctx, Issue{Name: "t-" + uuid.NewString()[:8], Plan: unlimited(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	if _, body := fetch(plain); strings.Contains(body, "tester") {
		t.Fatalf("an ordinary subscription is told it is a tester's: %s", body)
	}

	if err := s.SetTitle(ctx, name, strings.Repeat("я", MaxTitle+1)); !errors.Is(err, ErrTitle) {
		t.Fatalf("a long title: %v", err)
	}
	if err := s.SetTitle(ctx, name, "a\x00b"); !errors.Is(err, ErrTitle) {
		t.Fatalf("a control character: %v", err)
	}
	if err := s.SetFleet(ctx, name, []string{"no-such-node"}); !errors.Is(err, entry.ErrUnknownNode) {
		t.Fatalf("an unknown node: %v", err)
	}
}
