package entry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFrontSiteIsARealPageDrawnFromTheDomain(t *testing.T) {
	const domain = "pine6573.lol"
	site := string(frontSite(domain))

	if len(site) < 20*1024 {
		t.Fatalf("site is %d bytes, the owner's rule is at least 20 KB", len(site))
	}
	if !strings.HasPrefix(site, "<!doctype html>") || !strings.Contains(site, "</html>") {
		t.Fatal("not a whole HTML document")
	}
	if !strings.Contains(site, "pine6573") {
		t.Fatal("the domain's name is not on the page")
	}

	// Deterministic: a rerun of provisioning must not churn the page.
	if string(frontSite(domain)) != site {
		t.Fatal("the same domain drew two different pages")
	}
	// Different domains draw different pages: no byte-identical page to
	// fingerprint across addresses.
	if string(frontSite("otter2384.lol")) == site {
		t.Fatal("two domains drew the same page")
	}

	// The domain is escaped, never injected as markup.
	if strings.Contains(string(frontSite("<script>x</script>.lol")), "<script>x") {
		t.Fatal("the domain is not escaped into the page")
	}
}

// installFront uploads the cover site, hands the secret path over stdin (never
// an argument or the log), and runs the nginx script pointed at the edge's
// loopback front.
func TestInstallFrontKeepsTheSecretOffArgumentsAndTunnelsToLoopback(t *testing.T) {
	id, err := newXhttpIdentity(443, 10001, "pine6573.lol")
	if err != nil {
		t.Fatal(err)
	}
	rel := provRelay{name: "euw1-1", status: "pending", id: id}
	box := &fakeBox{}
	j := &jobRun{ctx: context.Background()}

	if err := j.installFront(&fakeRemote{b: box}, rel); err != nil {
		t.Fatal(err)
	}

	secret := id.Xhttp.Path
	var siteCmd, pathCmd, scriptCmd int = -1, -1, -1
	for i, cmd := range box.cmds {
		switch {
		case strings.Contains(cmd, "/var/www/pine6573.lol/index.html"):
			siteCmd = i
		case strings.Contains(cmd, "/run/nekko-front.path"):
			pathCmd = i
		case strings.HasPrefix(cmd, "bash -s -- pine6573.lol 10002"):
			scriptCmd = i
		}
		// The secret path is a credential: it never rides on a command line.
		if strings.Contains(cmd, secret) {
			t.Fatalf("the secret path is on a command line: %q", cmd)
		}
	}
	if siteCmd < 0 || pathCmd < 0 || scriptCmd < 0 {
		t.Fatalf("missing a step: site=%d path=%d script=%d (cmds %v)", siteCmd, pathCmd, scriptCmd, box.cmds)
	}

	if box.stdins[siteCmd] != string(frontSite("pine6573.lol")) {
		t.Fatal("the uploaded site is not the cover site")
	}
	if box.stdins[pathCmd] != secret {
		t.Fatalf("the secret path did not go to the tmpfs file over stdin")
	}
	// The script we send carries no secret (the box reads it from the file) and
	// forwards to the edge's loopback front, one past the Xray inbound.
	script := box.stdins[scriptCmd]
	if strings.Contains(script, secret) {
		t.Fatal("the script text carries the secret")
	}
	// The port is the script's $front argument, substituted on the box; the
	// command carries 10002 (asserted above), the script text carries $front.
	if !strings.Contains(script, "proxy_pass http://127.0.0.1:$front;") {
		t.Fatalf("the tunnel does not point at the loopback front:\n%s", script)
	}
	if !strings.Contains(script, "certbot certonly --webroot") || !strings.Contains(script, "listen 443 ssl http2;") {
		t.Fatal("the script is missing the certificate or the TLS front")
	}
}

// An identity moved to another domain keeps its VLESS id, secret path, mode and
// ports: only the name changes, and the original is left as it was.
func TestAnXhttpIdentityMovesToAnotherDomainAndKeepsItsSecrets(t *testing.T) {
	id, err := newXhttpIdentity(443, 10001, "pine6573.lol")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := id.withXhttpDomain("cirrus9867.lol")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Xhttp.Domain != "cirrus9867.lol" {
		t.Fatalf("domain = %q, want cirrus9867.lol", moved.Xhttp.Domain)
	}
	if moved.UUID != id.UUID || moved.Xhttp.Path != id.Xhttp.Path || moved.Xhttp.Mode != id.Xhttp.Mode ||
		moved.Port != id.Port || moved.LocalPortBase != id.LocalPortBase {
		t.Fatal("a domain move changed something besides the name")
	}
	if id.Xhttp.Domain != "pine6573.lol" {
		t.Fatal("the original identity was changed in place")
	}

	reality, err := newIdentity(443, 10001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reality.withXhttpDomain("x.lol"); !errors.Is(err, ErrRejected) {
		t.Fatalf("a Reality relay has no domain to move: err = %v", err)
	}
}

// A box given a replacement domain (evict_domain) moves its relays to it
// before anything is deployed; relays already on it are left alone.
func TestProvisionMovesRelaysToTheBoxsNewDomain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.xhttpBox()
	relay := h.relay(box, KindPersonal)
	before, err := h.svc.RelayIdentity(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}

	relays, err := h.svc.relayIdentities(ctx, box)
	if err != nil {
		t.Fatal(err)
	}
	j := &jobRun{p: &Provisioner{svc: h.svc}, ctx: ctx, box: box, acc: boxAccess{domain: "cirrus9867.lol"}}
	moved, err := j.moveRelaysToDomain(relays)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].id.Xhttp.Domain != "cirrus9867.lol" {
		t.Fatalf("relays after the move = %+v, want one on cirrus9867.lol", moved)
	}
	after, err := h.svc.RelayIdentity(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}
	if after.Xhttp.Domain != "cirrus9867.lol" || after.Xhttp.Path != before.Xhttp.Path || after.UUID != before.UUID {
		t.Fatalf("stored identity = %+v, want the same secrets on cirrus9867.lol", after.Xhttp)
	}
	if len(j.movedRelays) != 1 || j.movedRelays[0] != relay {
		t.Fatalf("movedRelays = %v, want [%s]", j.movedRelays, relay)
	}

	// A relay already on the box's domain is not touched, and nobody is told.
	j2 := &jobRun{p: &Provisioner{svc: h.svc}, ctx: ctx, box: box, acc: boxAccess{domain: "cirrus9867.lol"}}
	if _, err := j2.moveRelaysToDomain(moved); err != nil {
		t.Fatal(err)
	}
	if len(j2.movedRelays) != 0 {
		t.Fatalf("a relay on the right domain was reported moved: %v", j2.movedRelays)
	}
}
