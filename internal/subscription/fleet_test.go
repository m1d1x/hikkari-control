package subscription

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

type fakeFleet struct {
	records [][]byte
	nodes   map[string]entry.NodeName
}

func (f fakeFleet) ServingRecords(context.Context) ([][]byte, error) { return f.records, nil }

func (f fakeFleet) NodeNames(context.Context) (map[string]entry.NodeName, error) { return f.nodes, nil }

// The stand's links are every serving relay as a subscriber would get it —
// node name and knobs on the link — but dialling with the relay's own shared
// credential, since no account holds a key on a relay it was never granted.
// AmneziaWG has no link without an account's peer, and a record the hub
// cannot open is left out.
func TestFleetLinksAreEveryRelayWithItsOwnCredential(t *testing.T) {
	ours := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	theirs := entry.NewSigner("entry-1", bytes.Repeat([]byte{4}, 32))
	s := New(nil, &fakeBatch{}, []entry.Key{ours.Key()})
	fleet := fakeFleet{
		records: [][]byte{
			signed(t, ours, realityRecord()),
			signed(t, ours, xhttpRecord()),
			signed(t, ours, hysteriaRecord(strings.Repeat("cd", 32))),
			signed(t, ours, awgRecord()),
			signed(t, theirs, realityRecord()),
		},
		nodes: map[string]entry.NodeName{"ams-1": {Slug: "n1", Label: "amsterdam-1-001", Fingerprint: "chrome_155"}},
	}
	links, err := s.FleetLinks(context.Background(), fleet)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 {
		t.Fatalf("links %q, want reality, xhttp and hysteria2", links)
	}
	reality, _ := url.Parse(links[0])
	if reality.User.Username() != testUUID || reality.Fragment != "amsterdam-1-001" ||
		reality.Query().Get("fp") != "chrome_155" || reality.Query().Get("hikkari-node") != "n:n1" {
		t.Fatalf("reality link %s: want the relay's own UUID and its node's name and knobs", links[0])
	}
	if xhttp, _ := url.Parse(links[1]); xhttp.User.Username() != testUUID || xhttp.Query().Get("type") != "xhttp" {
		t.Fatalf("xhttp link %s", links[1])
	}
	if hy, _ := url.Parse(links[2]); hy.Scheme != "hysteria2" || hy.User.Username() != "0123456789abcdef0123" || hy.Query().Get("obfs") != "salamander" {
		t.Fatalf("hysteria2 link %s: want the sing-box form the stand's parser reads", links[2])
	}
}
