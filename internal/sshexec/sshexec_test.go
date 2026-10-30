package sshexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// server is an in-process ssh server: password "pw" or the one client key
// let you in; "exec" echoes stdin back prefixed with the command, and the
// command "fail" exits 3.
func server(t *testing.T, clientKey ssh.PublicKey) (addr string, hostKey ssh.Signer) {
	t.Helper()
	_, hk, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ = ssh.NewSignerFromKey(hk)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == "pw" {
				return nil, nil
			}
			return nil, errors.New("no")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && string(k.Marshal()) == string(clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("no")
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(c, cfg)
		}
	}()
	return ln.Addr().String(), hostKey
}

func serveConn(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type != "exec" {
					_ = r.Reply(false, nil)
					continue
				}
				cmd := string(r.Payload[4:])
				_ = r.Reply(true, nil)
				in, _ := io.ReadAll(ch)
				_, _ = io.WriteString(ch, cmd+":"+string(in))
				// The order sshd keeps: data, EOF, exit status, close.
				_ = ch.CloseWrite()
				code := byte(0)
				if cmd == "fail" {
					code = 3
				}
				_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, code})
				return
			}
		}()
	}
}

func TestTrustOnFirstUseAndAfter(t *testing.T) {
	_, ck, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(ck)
	addr, hostKey := server(t, signer.PublicKey())
	ctx := context.Background()

	c, err := Dial(ctx, Target{Addr: addr, User: "root"}, ssh.Password("pw"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey.PublicKey())))
	if c.HostKey() != want {
		t.Fatalf("host key %q, want %q", c.HostKey(), want)
	}
	out, err := c.Run(ctx, "cat", strings.NewReader("hi"))
	if err != nil || out != "cat:hi" {
		t.Fatalf("run: %q %v", out, err)
	}
	// A failing command is an error with its exit status; what it printed is
	// the output, never the error's text (audit 42 С3: errors go to the
	// hub's log, and a box's own output is the box's business).
	if out, err := c.Run(ctx, "fail", nil); err == nil || out != "fail:" ||
		strings.Contains(err.Error(), "fail:") || !strings.Contains(err.Error(), "3") {
		t.Fatalf("a failing command: %q %v", out, err)
	}
	c.Close()

	// With the key on record, the key login works…
	c, err = Dial(ctx, Target{Addr: addr, User: "root", HostKey: want}, ssh.PublicKeys(signer))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	// …another host key is refused, whatever the credentials.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	os, _ := ssh.NewSignerFromKey(other)
	otherKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(os.PublicKey())))
	if _, err := Dial(ctx, Target{Addr: addr, User: "root", HostKey: otherKey}, ssh.Password("pw")); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("changed host key: %v", err)
	}
	// A wrong password reads as an auth refusal, not a network fault.
	if _, err := Dial(ctx, Target{Addr: addr, User: "root"}, ssh.Password("nope")); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
}

// An error names the machine and never its address (audit 42 С3). The errors
// of this package end up in the hub's log, which goes to ClickHouse and the
// console's live log; the address is the secret the hub keeps sealed.
func TestErrorsNameTheMachineNotItsAddress(t *testing.T) {
	ctx := context.Background()

	// Nothing listens: the dial itself fails, and the net error underneath
	// carries the address in its own text.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	_, err = Dial(ctx, Target{Addr: closed, Name: "ams7", User: "root"}, ssh.Password("pw"))
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), "ams7") {
		t.Fatalf("dial error: %v", err)
	}

	// Something listens and hangs up mid-handshake.
	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "SSH-2.0-x\r\n")
			c.Close()
		}
	}()
	_, err = Dial(ctx, Target{Addr: ln.Addr().String(), Name: "ams7", User: "root"}, ssh.Password("pw"))
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), "ams7") {
		t.Fatalf("handshake error: %v", err)
	}
}

// Redact keeps the error inspectable: the sentinel survives the rewrite.
func TestRedactKeepsTheChain(t *testing.T) {
	err := Redact(fmt.Errorf("dial tcp 203.0.113.7:22: %w", ErrAuth), "203.0.113.7", "ams7")
	if strings.Contains(err.Error(), "203.0.113.7") || !strings.Contains(err.Error(), "ams7") || !errors.Is(err, ErrAuth) {
		t.Fatalf("redacted: %v (is ErrAuth: %v)", err, errors.Is(err, ErrAuth))
	}
	if Redact(nil, "203.0.113.7", "ams7") != nil {
		t.Fatal("nil must stay nil")
	}
}

// Audit 42 recheck НН5: Redact replaced the address as it was typed, and the
// errors from below print it the way net does — an IPv6 address written with
// capitals or zero groups stayed in the text.
func TestRedactCatchesTheCanonicalAddress(t *testing.T) {
	for _, typed := range []string{"2001:DB8:0:0::1", "2001:db8:0000::1", "2001:db8::1"} {
		err := Redact(errors.New("dial tcp [2001:db8::1]:22: i/o timeout"), typed, "ams7")
		if strings.Contains(err.Error(), "2001:db8::1") || !strings.Contains(err.Error(), "ams7") {
			t.Errorf("typed %q: %v", typed, err)
		}
	}
}
