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

// A node is reported under the slug the client derives from the link the
// subscription renders for it (subscription.Link, AwgLink), hashed with the
// link's address. The slugs are what hikkari-client profile.ProtoSlug returns
// for those links.
func TestFleetClientIDIsTheClientsIDForTheLink(t *testing.T) {
	for proto, slug := range map[string]string{
		"vless-reality-vision": "vless-reality",
		"vless-xhttp":          "vless-xhttp",
		"hysteria2":            "hysteria2",
		"amneziawg":            "wireguard",
	} {
		if got, want := FleetClientID("1.2.3.4", 8443, proto), ServerID("1.2.3.4", 8443, slug); got != want {
			t.Fatalf("%s: %s, want %s", proto, got, want)
		}
	}
	for _, c := range []struct {
		host  string
		port  int
		proto string
	}{{"", 443, "hysteria2"}, {"1.2.3.4", 0, "hysteria2"}, {"1.2.3.4", 443, "trojan"}} {
		if got := FleetClientID(c.host, c.port, c.proto); got != "" {
			t.Fatalf("%+v: %s, want none", c, got)
		}
	}
}
