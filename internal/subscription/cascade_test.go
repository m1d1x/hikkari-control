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

// cascadeBatch is a fleet with one exit whose Hikkari relay the trunk reaches.
type cascadeBatch struct {
	fakeBatch
	entryAddr string
	relays    map[string]bool
}

// BoxRecords is every record the fleet holds, in the pool or not.
func (c *cascadeBatch) BoxRecords(context.Context, string) ([][]byte, error) { return c.envelopes, nil }

func (c *cascadeBatch) CascadeVia(_ context.Context, entryBox, exitBox string) (string, map[string]bool, error) {
	if entryBox != "ruin1" || exitBox != "unl1" {
		return "", nil, entry.ErrRejected
	}
	return c.entryAddr, c.relays, nil
}

// NodeNamesFor names the exit relay's node as the fleet would.
func (c *cascadeBatch) NodeNamesFor(context.Context, uint32) (map[string]entry.NodeName, error) {
	return map[string]entry.NodeName{"unl1-2": {Slug: "ams-node", Label: "амстердам-001"}}, nil
}

func hikkariRecord(name, addr string) entry.RelayRecord {
	return entry.RelayRecord{Name: name, Proto: entry.ProtoHikkari, Address: addr, Port: 443,
		HikkariParams: &entry.HikkariParams{Host: "exit.example", User: "probe", Pass: "0123456789abcdef0123"}}
}

// A cascade account is served only the exit's Hikkari relays, each dialling
// the entry's address and port with the relay's own SNI and the account's
// key, named by the exit's node and country, and with no node id on the link; dropping the cascade brings its relays
// back as they are.
func TestACascadeAccountDialsTheExitThroughTheEntry(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	batch := &cascadeBatch{
		fakeBatch: fakeBatch{envelopes: [][]byte{
			signed(t, signer, realityRecord()),
			signed(t, signer, hikkariRecord("unl1-2", "198.51.100.4")),
			signed(t, signer, hikkariRecord("other-1", "198.51.100.9")),
		}},
		entryAddr: "192.0.2.10",
		relays:    map[string]bool{"unl1-2": true},
	}
	s := New(pool, batch, []entry.Key{signer.Key()})
	ctx := context.Background()
	for box, location := range map[string]string{"ruin1": "Moscow, RU", "unl1": "Amsterdam, NL"} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, '\x00', 'test', $2, 'hikkari') ON CONFLICT (name) DO UPDATE SET location = $2`, box, location); err != nil {
			t.Fatal(err)
		}
	}
	name := "t-" + uuid.NewString()[:8]
	token, err := s.Add(ctx, name, unlimited(t, s))
	if err != nil {
		t.Fatal(err)
	}
	get := func() []string {
		req := httptest.NewRequest(http.MethodGet, "/sub/"+token, nil)
		req.SetPathValue("token", token)
		req.Header.Set(featuresHeader, "hikkari, hikkari-dup")
		rec := httptest.NewRecorder()
		s.Handler()(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		raw, err := base64.StdEncoding.DecodeString(rec.Body.String())
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(string(raw), "\n")
	}

	if link, err := s.URLOf(ctx, name); err != nil || !strings.HasSuffix(link, "/sub/"+token) {
		t.Fatalf("the link read back is %q (%v), want the one handed out", link, err)
	}
	if _, err := s.URLOf(ctx, "no-such-account"); err != ErrUnknown {
		t.Fatalf("an unknown account's link: %v", err)
	}

	if err := s.SetCascade(ctx, name, "unl1", "nowhere", 8443); err == nil {
		t.Fatal("a cascade through an entry the fleet cannot say was set")
	}
	if err := s.SetCascade(ctx, name, "unl1", "ruin1", 8443); err != nil {
		t.Fatal(err)
	}
	links := get()
	if len(links) != 1 {
		t.Fatalf("links %q, want the exit's Hikkari relay only", links)
	}
	u, err := url.Parse(links[0])
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := u.User.Password()
	if u.Scheme != "hikkari+v1" || u.Host != "192.0.2.10:8443" || u.Query().Get("sni") != "exit.example" ||
		u.Query().Get("dup") != "1" || u.Query().Get("hikkari-node") != "" || pass == "0123456789abcdef0123" ||
		u.Fragment != "🇳🇱 амстердам-001" {
		t.Fatalf("cascade link %s", links[0])
	}
	var all bool
	if err := pool.QueryRow(ctx, `SELECT all_relays FROM hub_accounts WHERE name = $1`, name).Scan(&all); err != nil || !all {
		t.Fatalf("a cascade account must hold the whole fleet, so the exit relay knows its key: %v %v", all, err)
	}

	if err := s.DropCascade(ctx, name); err != nil {
		t.Fatal(err)
	}
	if links := get(); len(links) != 3 || strings.Contains(strings.Join(links, "\n"), "192.0.2.10") {
		t.Fatalf("after the cascade: %q, want the relays as they are", links)
	}
	if err := s.DropCascade(ctx, name); err == nil {
		t.Fatal("dropping a cascade that is not there succeeded")
	}
}

func TestCountryFlagIsTheLocationsISOCode(t *testing.T) {
	for location, want := range map[string]string{
		"Amsterdam, NL":          "🇳🇱",
		"Netherlands, nl":        "🇳🇱",
		"Riga, LV":               "🇱🇻",
		"de":                     "",
		"Amsterdam":              "",
		"Amsterdam, Netherlands": "",
		"":                       "",
	} {
		if got := countryFlag(location); got != want {
			t.Errorf("countryFlag(%q) = %q, want %q", location, got, want)
		}
	}
}

// The watch's probe links: the exit's Hikkari relay straight and through the
// entry, under the account's key, named by path in ASCII.
func TestCascadeProbeLinksAreTheExitStraightAndThroughTheEntry(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	batch := &cascadeBatch{
		fakeBatch: fakeBatch{envelopes: [][]byte{
			signed(t, signer, realityRecord()),
			signed(t, signer, hikkariRecord("unl1-2", "198.51.100.4")),
		}},
		entryAddr: "192.0.2.10",
		relays:    map[string]bool{"unl1-2": true},
	}
	s := New(pool, batch, []entry.Key{signer.Key()})
	ctx := context.Background()
	name := "w-" + uuid.NewString()[:8]
	if _, err := s.Add(ctx, name, unlimited(t, s)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CascadeProbeLinks(ctx, name, "ruin1", "unl1", 8443); err == nil {
		t.Fatal("an account without the whole fleet was given probe links")
	}
	if _, err := pool.Exec(ctx, `UPDATE hub_accounts SET all_relays = true WHERE name = $1`, name); err != nil {
		t.Fatal(err)
	}
	asked := len(batch.asked)
	links, err := s.CascadeProbeLinks(ctx, name, "ruin1", "unl1", 8443)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.asked) != asked+1 {
		t.Fatal("the probe links were rendered without a fetch of the account's bundle: its grants lapse with the first drain")
	}
	if len(links) != 2 {
		t.Fatalf("links %q", links)
	}
	want := map[string]string{"cascade-direct-unl1-2": "198.51.100.4:443", "cascade-entry-unl1-2": "192.0.2.10:8443"}
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		pass, _ := u.User.Password()
		if want[u.Fragment] != u.Host || u.Query().Get("dup") != "1" || pass == "0123456789abcdef0123" || pass == "" {
			t.Fatalf("probe link %s", l)
		}
		delete(want, u.Fragment)
	}
}
