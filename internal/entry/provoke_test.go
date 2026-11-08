package entry

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// provokeRecorder records what standProvoke runs, answering uname and sha256sum.
type provokeRecorder struct {
	have  string
	cmds  []string
	stdin map[string]string
}

func (r *provokeRecorder) Run(_ context.Context, cmd string, stdin io.Reader) (string, error) {
	r.cmds = append(r.cmds, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		r.stdin[cmd] = string(b)
	}
	switch {
	case cmd == "uname -m":
		return "x86_64\n", nil
	case strings.HasPrefix(cmd, "sha256sum"):
		return r.have + "\n", nil
	case strings.HasPrefix(cmd, "bash -s -- "):
		return "nekko-provoke слушает tcp и udp 2053\n", nil
	}
	return "", nil
}
func (r *provokeRecorder) HostKey() string { return "" }
func (r *provokeRecorder) Close() error    { return nil }

func testBuild(t *testing.T) map[string]EdgeBuild {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nekko-edge")
	body := []byte("edge binary")
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return map[string]EdgeBuild{"amd64": {Arch: "amd64", SHA: hex.EncodeToString(sum[:]), Size: int64(len(body)), path: path}}
}

func TestStandProvokeInstallsItsOwnBinaryAndUnit(t *testing.T) {
	builds := testBuild(t)
	r := &provokeRecorder{stdin: map[string]string{}}
	if err := standProvoke(context.Background(), r, builds, 2053, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.cmds, "\n")
	if r.stdin["cat > "+provokeBinary+".new"] != "edge binary" {
		t.Fatalf("binary not staged to its own path:\n%s", joined)
	}
	if strings.Contains(joined, "/usr/local/bin/nekko-edge ") || strings.Contains(joined, "/usr/local/bin/nekko-edge.new") {
		t.Fatalf("touches the relays' binary:\n%s", joined)
	}
	env := r.stdin["cat > "+provokeEnvFile+".new"]
	if !strings.Contains(env, "EDGE_PROTO=provoke") || !strings.Contains(env, "EDGE_PROVOKE_LISTEN=0.0.0.0:2053") {
		t.Fatalf("env %q", env)
	}
	if !strings.Contains(r.stdin["cat > /etc/systemd/system/nekko-provoke.service"], "ExecStart="+provokeBinary+" run") {
		t.Fatal("unit does not run the provoke binary")
	}
	if r.stdin["bash -s -- 2053"] != provokeSwapScript {
		t.Fatal("swap script not run with the port")
	}
}

func TestStandProvokeSkipsUploadOfTheSameBuild(t *testing.T) {
	builds := testBuild(t)
	r := &provokeRecorder{stdin: map[string]string{}, have: builds["amd64"].SHA}
	if err := standProvoke(context.Background(), r, builds, 2053, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, staged := r.stdin["cat > "+provokeBinary+".new"]; staged {
		t.Fatal("uploaded a build the box already has")
	}
}

func TestStandProvokeRefusesAnArchWithoutBuild(t *testing.T) {
	r := &provokeRecorder{stdin: map[string]string{}}
	if err := standProvoke(context.Background(), r, map[string]EdgeBuild{}, 2053, func(string, ...any) {}); err == nil {
		t.Fatal("deployed without a build")
	}
}

// fakeReceiver answers the receiver's wire the way edge/provoke.go does, for
// checkProvoke only.
func fakeReceiver(t *testing.T, sawHello bool) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	var mu sync.Mutex
	udp := map[string]int{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := net.ResolveUDPAddr("udp", ln.Addr().String())
	ul, err := net.ListenUDP("udp", ua)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); ul.Close() })
	seenTag := make(chan string, 1)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := ul.ReadFromUDP(buf)
			if err != nil {
				return
			}
			mu.Lock()
			udp[hex.EncodeToString(buf[4:12])]++
			mu.Unlock()
			_, _ = ul.WriteToUDP(buf[:n], from)
		}
	}()
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		c := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(hi *tls.ClientHelloInfo) (*tls.Config, error) {
			tag := strings.TrimPrefix(hi.SupportedProtos[0], "hk-")
			seenTag <- tag
			return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: hi.SupportedProtos}, nil
		}})
		if c.Handshake() != nil {
			return
		}
		tag := <-seenTag
		if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
			return
		}
		mu.Lock()
		n := udp[tag]
		mu.Unlock()
		fmt.Fprintf(c, `{"%s":{"hello":%t,"udp":%d}}`+"\n", tag, sawHello, n)
	}()
	return ln.Addr().String()
}

func TestCheckProvokeAcceptsAnAnsweringReceiver(t *testing.T) {
	if err := checkProvoke(context.Background(), fakeReceiver(t, true)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckProvokeRefusesAReceiverThatSawNothing(t *testing.T) {
	if err := checkProvoke(context.Background(), fakeReceiver(t, false)); err == nil {
		t.Fatal("a receiver that saw no hello passed")
	}
}

func TestCheckProvokeRefusesASilentPort(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := checkProvoke(ctx, addr); err == nil {
		t.Fatal("a dead port passed")
	}
}
