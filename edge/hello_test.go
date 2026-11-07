package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// releaseHellos are copies of hikkari-client's testdata/hello (recorded from
// the release's core c06e742b5717 and from Chromium 152), with the ja4 and hk
// the client's wireGolden pins for them. The node must hash them the same, or
// its counters cannot be read against the release.
var releaseHellos = map[string]struct{ ja4, hk string }{
	"tls-chrome":                  {"t13d1516h2_8daaf6152771_d8a2da3f94cd", "h1e431ec7444af069a"},
	"tls-chrome_155":              {"t13d1517h2_8daaf6152771_cb7bf5808d99", "h1121e2b99d2744911"},
	"tls-firefox":                 {"t13d1715h2_5b57614c22b0_a54fffd0eb61", "h1a7f29bb57cd8c350"},
	"tls-safari":                  {"t13d2013h2_a09f3c656075_7f0f34a4126d", "h1c4b9dfb6a023e46d"},
	"tls-go":                      {"t13d131100_f57a46bbacb6_ab7e3b40a677", "h11c409d9ffa0e9507"},
	"reality-chrome":              {"t13d1516h2_8daaf6152771_d8a2da3f94cd", "h1e431ec7444af069a"},
	"reality-chrome-classical":    {"t13d1516h2_8daaf6152771_d8a2da3f94cd", "h13ba089e2b415f898"},
	"xhttp-chrome":                {"t13d1516h2_8daaf6152771_d8a2da3f94cd", "h1e431ec7444af069a"},
	"browser-chromium-152-pane-a": {"t13d1516h2_8daaf6152771_806a8c22fdea", "h1e08906f1ba730b11"},
}

func readHello(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hello", name+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHelloHKMatchesTheRelease(t *testing.T) {
	for name, want := range releaseHellos {
		h, err := parseClientHello(readHello(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if h.hk() != want.hk || h.ja4() != want.ja4 {
			t.Errorf("%s: hk %s ja4 %s, want %s %s", name, h.hk(), h.ja4(), want.hk, want.ja4)
		}
	}
	h, _ := parseClientHello(readHello(t, "tls-chrome"))
	if h.ServerName != "wire-mirror.example" {
		t.Fatalf("server name %q", h.ServerName)
	}
	if b, _ := parseClientHello(readHello(t, "browser-chromium-152-pane-a")); b.ServerName != "localhost" {
		t.Fatalf("browser server name %q", b.ServerName)
	}
}

// A hello split over records of any size reads whole.
func TestReadClientHelloAcrossRecords(t *testing.T) {
	msg := readHello(t, "tls-chrome_155")
	var wire []byte
	for i := 0; i < len(msg); i += 3 {
		end := min(i+3, len(msg))
		wire = append(wire, 22, 3, 1, 0, byte(end-i))
		wire = append(wire, msg[i:end]...)
	}
	got, records, err := readClientHello(bytes.NewReader(wire))
	if err != nil || !bytes.Equal(got, msg) || records != (len(msg)+2)/3 {
		t.Fatalf("%d records, %v", records, err)
	}
}

func TestParseClientHelloSurvivesEveryCut(t *testing.T) {
	msg := readHello(t, "tls-firefox")
	for i := 0; i < len(msg); i++ {
		_, _ = parseClientHello(msg[:i])
		broken := append([]byte(nil), msg...)
		broken[i] ^= 0xff
		_, _ = parseClientHello(broken)
	}
}
