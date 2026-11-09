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

func (c *cascadeBatch) CascadeVia(_ context.Context, entryBox, exitBox string) (string, map[string]bool, error) {
	if entryBox != "ruin1" || exitBox != "unl1" {
		return "", nil, entry.ErrRejected
	}
	return c.entryAddr, c.relays, nil
}

func hikkariRecord(name, addr string) entry.RelayRecord {
	return entry.RelayRecord{Name: name, Proto: entry.ProtoHikkari, Address: addr, Port: 443,
		HikkariParams: &entry.HikkariParams{Host: "exit.example", User: "probe", Pass: "0123456789abcdef0123"}}
}

// A cascade account is served only the exit's Hikkari relays, each dialling
// the entry's address and port with the relay's own SNI and the account's
// key, and with no node on the link; dropping the cascade brings its relays
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
	for _, box := range []string{"ruin1", "unl1"} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, '\x00', 'test', 'de', 'hikkari') ON CONFLICT (name) DO NOTHING`, box); err != nil {
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
		u.Fragment != "Hikkari unl1-2 через вход" {
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
