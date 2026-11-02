package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// clientRUZone is the client's copy of the denylist (hikkari-client
// engine/core/ruzone): the client sends exactly this set direct, because the
// edge refuses it. Checked only with both repositories side by side.
const clientRUZone = "../../hikkari-client/engine/core/ruzone"

func TestClientSendsDirectWhatTheEdgeRefuses(t *testing.T) {
	domainsTxt, err := os.ReadFile(filepath.Join(clientRUZone, "domains.txt"))
	if os.IsNotExist(err) {
		t.Skip("hikkari-client is not checked out next to hikkari-control")
	}
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, d := range blockedDomains {
		want = append(want, strings.TrimPrefix(d, "domain:"))
	}
	if got := strings.Fields(string(domainsTxt)); !slices.Equal(got, want) {
		t.Errorf("client domains.txt differs from blockedDomains:\n got %v\nwant %v", got, want)
	}

	gz, err := os.ReadFile(filepath.Join(clientRUZone, "cidrs.txt.gz"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(clientRUZone, "ruzone.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(raw))
	for _, extra := range blockedRUNets[len(got):] {
		if !bytes.Contains(src, []byte(`"`+extra+`"`)) {
			t.Errorf("client ruzone.go has no %s", extra)
		}
	}
	if !slices.Equal(got, blockedRUNets[:min(len(got), len(blockedRUNets))]) {
		t.Error("client cidrs.txt.gz differs from ru_cidrs.txt.gz")
	}
}
