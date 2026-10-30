package downdetector

import "testing"

// The value is pinned independently of this code
// (printf 'example.com:443:vless-reality-xhttp' | shasum -a 256 | cut -c1-32)
// and must match hikkari-client engine/core/downdetector.ServerID.
func TestServerIDMatchesClient(t *testing.T) {
	const want = "s:7a19192edd834db17881c0ad69c22fa9"
	for _, host := range []string{"example.com", "Example.COM"} {
		if got := ServerID(host, 443, "vless-reality-xhttp"); got != want {
			t.Fatalf("ServerID(%q) = %s, want %s", host, got, want)
		}
	}
	if !ValidServerID(want) {
		t.Fatal("pinned id does not pass validation")
	}
}

func TestNodeIDOverridesHash(t *testing.T) {
	n := Node{Name: "fra", Host: "1.2.3.4", Port: 443, Proto: "vless-reality", Slug: "fra-1"}
	if n.ID() != "n:fra-1" {
		t.Fatalf("id = %s", n.ID())
	}
	n.Slug = ""
	if n.ID() != ServerID("1.2.3.4", 443, "vless-reality") {
		t.Fatalf("id = %s", n.ID())
	}
}
