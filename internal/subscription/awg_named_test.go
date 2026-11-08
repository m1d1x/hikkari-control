package subscription

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// awgDescriptionOf reads the server description inside a vpn:// link.
func awgDescriptionOf(t *testing.T, link string) string {
	t.Helper()
	body := strings.SplitN(strings.SplitN(strings.TrimPrefix(link, "vpn://"), "#", 2)[0], "?", 2)[0]
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw[4:]))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	return got.Description
}

// A subscriber's AWG link reads as its node, like every other link: the
// node's name where clients take the title from, its slug on the link. A
// tester saw "Hikkari awgl1-1" (the relay) where the others read
// амстердам-001.
func TestAwgLinkCarriesTheNodesName(t *testing.T) {
	node := entry.NodeName{Slug: "d20957fe", Label: "амстердам-001"}
	rendered, err := awgLinkFor(node)(awgRecord())
	if err != nil {
		t.Fatal(err)
	}
	link := namedLink(rendered, node, nil)
	if got := awgDescriptionOf(t, link); got != "амстердам-001" {
		t.Errorf("description = %q, want the node's name", got)
	}
	if got := capacity.NodeOf(link); got != "d20957fe" {
		t.Errorf("node on the link = %q", got)
	}
	plain, _ := AwgLink(awgRecord())
	if capacity.Key(link) != capacity.Key(plain) {
		t.Error("naming the link changed its capacity key: it would lose its measurements")
	}
	if got := awgDescriptionOf(t, plain); got != "Hikkari eun20-1" {
		t.Errorf("a node with no name keeps the relay's, got %q", got)
	}
}
