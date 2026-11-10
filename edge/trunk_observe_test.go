package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/m1d1x/hikkari-protocol/trunk"
)

// TestTrunkObservesTesters: a client's Hikkari connection carried by the
// trunk to the relay itself, as through the cascade's entry. A tester's key
// (the hub's observe list) marks its stream: both ends report it under one
// id, the exit with the key, the entry without; the exit's rows wait for the
// pulse. Another subscriber's key, through the same trunk, leaves no report
// at either end.
func TestTrunkObservesTesters(t *testing.T) {
	const (
		trunkKey  = "trunk-key-0123456789abcdef-xyz"
		testerKey = "tester-0123456789abcdef0123456789"
		userKey   = "user-0123456789abcdef0123456789ab"
	)
	certPEM, keyPEM := hikkariTestCert(t, "hk.example")
	relay := freeAddr(t)
	_, relayPort, _ := net.SplitHostPort(relay)
	storage, root := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>site</html>"), 0644)
	get := func(k string) string {
		return map[string]string{
			"EDGE_PROTO": protoHikkari, "EDGE_LISTEN": relay, "EDGE_HIKKARI_HOST": "hk.example",
			"EDGE_HIKKARI_USER": "probe", "EDGE_HIKKARI_PASS": "probe-secret-long-enough", "EDGE_HIKKARI_ROOT": root,
			"STATE_DIRECTORY": storage, "EDGE_HIKKARI_TRUNK_KEYS": trunkKey, "EDGE_HIKKARI_TRUNK_PORTS": relayPort,
		}[k]
	}
	cfg, err := naiveFromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	n := newNaiveRuntime(cfg)
	_ = n.Set(context.Background(), []string{testerKey, userKey})
	n.setObserved([]string{testerKey})
	activeNaive.Store(n)
	defer activeNaive.Store(nil)
	raw, _ := cfg.document()
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["storage"] = map[string]any{"module": "file_system", "root": t.TempDir()}
	apps := doc["apps"].(map[string]any)
	apps["tls"] = map[string]any{"certificates": map[string]any{"load_pem": []any{map[string]any{"certificate": certPEM, "key": keyPEM}}}}
	server := apps["http"].(map[string]any)["servers"].(map[string]any)["naive"].(map[string]any)
	server["automatic_https"] = map[string]any{"disable": true}
	raw, _ = json.Marshal(doc)
	if err = caddy.Load(raw, true); err != nil {
		t.Fatal(err)
	}
	defer caddy.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.startTrunkExit(ctx)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(certPEM))

	entryReports := make(chan trunk.Report, 16)
	ports, _ := trunk.NewPortBook(43000, 44000, time.Minute)
	entry, err := trunk.NewEntry(trunk.Config{Observe: func(r trunk.Report) { entryReports <- r }}, 2,
		func(ctx context.Context, port int) (io.ReadWriteCloser, error) {
			return goMember(relay, roots, trunkKey, port)
		}, ports, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	go entry.Run(ctx)
	deadline := time.Now().Add(15 * time.Second)
	for entry.Session().Stats().Members < 2 {
		if time.Now().After(deadline) {
			t.Fatal("pool did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	port, _ := strconv.Atoi(relayPort)

	// A client through the entry: its TLS to the relay rides the trunk. The
	// CONNECT's destination does not matter here (the relay refuses
	// loopback): what counts is the key it was admitted with.
	through := func(key string) {
		client, edgeSide := net.Pipe()
		go entry.Session().Carry(edgeSide, uint16(port))
		tun, _ := goConnect(client, roots, "hikkari", key, "127.0.0.1:9")
		time.Sleep(300 * time.Millisecond)
		if tun != nil {
			tun.Close()
		}
		client.Close()
	}

	through(userKey)
	time.Sleep(500 * time.Millisecond)
	select {
	case r := <-entryReports:
		t.Fatalf("a subscriber's stream was reported by the entry: %+v", r)
	default:
	}
	if rows := n.takeLegs(); len(rows) != 0 {
		t.Fatalf("a subscriber's stream was reported by the exit: %+v", rows)
	}

	through(testerKey)
	var mark, end trunk.Report
	for _, want := range []string{trunk.PhaseMark, trunk.PhaseEnd} {
		select {
		case r := <-entryReports:
			if r.Phase != want {
				t.Fatalf("entry report %+v, want %s", r, want)
			}
			if want == trunk.PhaseMark {
				mark = r
			} else {
				end = r
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no entry %s report", want)
		}
	}
	if mark.ID == 0 || end.ID != mark.ID || mark.Tag != "" || mark.Up == 0 {
		t.Fatalf("entry mark %+v end %+v", mark, end)
	}
	deadline = time.Now().Add(5 * time.Second)
	var rows []legRow
	for len(rows) < 2 && time.Now().Before(deadline) {
		rows = append(rows, n.takeLegs()...)
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != 2 {
		t.Fatalf("exit rows %+v", rows)
	}
	id := exitLeg(trunk.Report{ID: mark.ID}, time.Now()).ID
	for i, phase := range []string{trunk.PhaseMark, trunk.PhaseEnd} {
		if r := rows[i]; r.ID != id || r.Side != "exit" || r.Key != testerKey || r.Phase != phase || r.Kind != "stream" {
			t.Fatalf("exit row %d %+v, id %s", i, r, id)
		}
	}
	// Up from the client arrived whole; down, the client hung up before the
	// relay's last bytes reached it.
	if rows[1].Down != end.Up || end.Down > rows[1].Up || end.Down == 0 {
		t.Fatalf("the ends disagree: exit down %d up %d, entry up %d down %d", rows[1].Down, rows[1].Up, end.Up, end.Down)
	}
	if n.targets.len() != 0 {
		t.Fatalf("%d loopback connections still tracked after their streams ended", n.targets.len())
	}
}
