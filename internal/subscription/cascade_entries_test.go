package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// A cascade's further entries (protocol v1.07) are named in entries= of its
// links, in the order they were added, only for a client that says it dials
// them: an older client refuses a link with a parameter it does not know.
func TestACascadeLinkNamesItsFurtherEntriesForAClientThatDialsThem(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	batch := &cascadeBatch{
		fakeBatch: fakeBatch{envelopes: [][]byte{signed(t, signer, hikkariRecord("unl1-2", "198.51.100.4"))}},
		entryAddr: "192.0.2.10",
		relays:    map[string]bool{"unl1-2": true},
		more:      map[string]string{"ruin2": "203.0.113.20", "ruin3": "203.0.113.30"},
	}
	s := New(pool, batch, []entry.Key{signer.Key()})
	ctx := context.Background()
	for _, box := range []string{"ruin1", "ruin2", "ruin3", "unl1"} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, '\x00', 'test', 'Moscow, RU', 'hikkari') ON CONFLICT (name) DO NOTHING`, box); err != nil {
			t.Fatal(err)
		}
	}
	name := "t-" + uuid.NewString()[:8]
	token, err := s.Add(ctx, name, unlimited(t, s))
	if err != nil {
		t.Fatal(err)
	}
	get := func(features string) *url.URL {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/sub/"+token, nil)
		req.SetPathValue("token", token)
		req.Header.Set(featuresHeader, features)
		rec := httptest.NewRecorder()
		s.Handler()(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		raw, _ := base64.StdEncoding.DecodeString(rec.Body.String())
		links := strings.Split(string(raw), "\n")
		if len(links) != 1 {
			t.Fatalf("links %q", links)
		}
		u, err := url.Parse(links[0])
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	if _, err := s.SetCascadeEntry(ctx, name, "ruin2", 9443); err == nil {
		t.Fatal("an entry was added to an account with no cascade")
	}
	if err := s.SetCascade(ctx, name, "unl1", "ruin1", 8443); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCascadeEntry(ctx, name, "ruin1", 8443); err == nil {
		t.Fatal("entry 0 was added again as a further entry")
	}
	if _, err := s.SetCascadeEntry(ctx, name, "nowhere", 9443); err == nil {
		t.Fatal("an entry whose trunk the fleet cannot say was added")
	}
	for i, want := range []struct {
		box  string
		port int
		n    int
	}{{"ruin2", 9443, 1}, {"ruin3", 443, 2}, {"ruin2", 9443, 1}} {
		n, err := s.SetCascadeEntry(ctx, name, want.box, want.port)
		if err != nil || n != want.n {
			t.Fatalf("add %d: entry %d, %v; want %d", i, n, err, want.n)
		}
	}

	if u := get("hikkari, hikkari-dup"); u.Query().Has("entries") || u.Host != "192.0.2.10:8443" {
		t.Fatalf("a client that dials one entry got %s", u)
	}
	u := get("hikkari, hikkari-dup, hikkari-entries")
	if got := u.Query().Get("entries"); got != "203.0.113.20:9443,203.0.113.30:443" || u.Host != "192.0.2.10:8443" {
		t.Fatalf("link %s, entries %q", u, got)
	}

	if err := s.DropCascadeEntry(ctx, name, "ruin2"); err != nil {
		t.Fatal(err)
	}
	if got := get("hikkari, hikkari-dup, hikkari-entries").Query().Get("entries"); got != "203.0.113.30:443" {
		t.Fatalf("entries after ruin2 left: %q", got)
	}
	if err := s.DropCascadeEntry(ctx, name, "ruin2"); err == nil {
		t.Fatal("an entry not there was dropped")
	}
	// A further entry the fleet can no longer say leaves the link, not the
	// subscription.
	delete(batch.more, "ruin3")
	if u := get("hikkari, hikkari-dup, hikkari-entries"); u.Query().Has("entries") {
		t.Fatalf("an entry the fleet cannot say stayed: %s", u)
	}
	// Dropping the cascade drops its entries.
	if err := s.DropCascade(ctx, name); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM entry_cascade_entries e JOIN hub_accounts a ON a.id = e.account_id
		WHERE a.name = $1`, name).Scan(&left); err != nil || left != 0 {
		t.Fatalf("entries left after the cascade: %d, %v", left, err)
	}
}

// The DE profile of a cascade with countries names each further entry at its
// own DE port, and leaves out an entry with no DE country there.
func TestTheDEProfileNamesFurtherEntriesAtTheirDEPorts(t *testing.T) {
	c := &cascade{address: "192.0.2.10", port: 8443, relays: map[string]bool{"r": true}, more: []cascadeEntry{
		{address: "203.0.113.20", port: 9443, dePort: 9444, relays: map[string]bool{"r": true}},
		{address: "203.0.113.30", port: 443, relays: map[string]bool{"r": true}},
		{address: "203.0.113.40", port: 443, dePort: 444, relays: map[string]bool{"other": true}},
	}}
	if got := c.entries("r", false); got != "203.0.113.20:9443,203.0.113.30:443" {
		t.Fatalf("NL entries %q", got)
	}
	if got := c.entries("r", true); got != "203.0.113.20:9444" {
		t.Fatalf("DE entries %q", got)
	}
	nl := "hikkari+v1://hikkari:k@192.0.2.10:8443?entries=203.0.113.20%3A9443%2C203.0.113.30%3A443&sni=x#n"
	links := countryPorts{de: 8444, sni: "core.example"}.links(nl, c.entries("r", true))
	if len(links) != 2 {
		t.Fatalf("links %q", links)
	}
	first, _ := url.Parse(links[0])
	second, _ := url.Parse(links[1])
	if first.Query().Get("entries") != "203.0.113.20:9443,203.0.113.30:443" || second.Query().Get("entries") != "203.0.113.20:9444" ||
		second.Port() != "8444" || second.Query().Get("egress") != "de" {
		t.Fatalf("NL %s, DE %s", links[0], links[1])
	}
	if de, _ := url.Parse(countryPorts{de: 8444}.links(nl, "")[1]); de.Query().Has("entries") {
		t.Fatalf("a DE link with no DE entries kept NL's: %s", de)
	}
}
