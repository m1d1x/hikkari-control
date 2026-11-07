package entry

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/sshexec"
	"golang.org/x/crypto/ssh"
)

type bootstrapRemote struct {
	cmd    string
	closed bool
}

func (r *bootstrapRemote) Run(_ context.Context, cmd string, _ io.Reader) (string, error) {
	r.cmd = cmd
	return "", nil
}
func (r *bootstrapRemote) Close() error    { r.closed = true; return nil }
func (r *bootstrapRemote) HostKey() string { return "pinned-owner-host" }
func bootstrapKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}
func TestOwnerAgentBootstrapProvesHubKeyAndPinsHost(t *testing.T) {
	r := &bootstrapRemote{}
	proof := &bootstrapRemote{}
	calls := 0
	base := func(_ context.Context, target sshexec.Target, _ ...ssh.AuthMethod) (Remote, error) {
		calls++
		switch calls {
		case 1:
			return nil, sshexec.ErrAuth
		case 2:
			return r, nil
		default:
			if target.HostKey != "pinned-owner-host" {
				t.Fatal("proof login lost host key pin")
			}
			return proof, nil
		}
	}
	key := bootstrapKey(t)
	got, err := OwnerBootstrapDialer(base, ssh.Password("fake-owner-auth"), key)(context.Background(), sshexec.Target{}, ssh.Password("fake-hub-auth"))
	if err != nil || got != proof || calls != 3 {
		t.Fatalf("proof %v calls=%d", err, calls)
	}
	if !r.closed || !strings.Contains(r.cmd, key) {
		t.Fatal("owner connection did not install key and close")
	}
}
func TestOwnerAgentOnlyRunsAfterAuthenticationFailure(t *testing.T) {
	for _, want := range []error{nil, sshexec.ErrHostKeyChanged, context.DeadlineExceeded} {
		calls := 0
		r := &bootstrapRemote{}
		base := func(context.Context, sshexec.Target, ...ssh.AuthMethod) (Remote, error) { calls++; return r, want }
		_, err := OwnerBootstrapDialer(base, ssh.Password("fake"), bootstrapKey(t))(context.Background(), sshexec.Target{})
		if !errors.Is(err, want) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}
func TestOwnerAgentRejectsInjectedPublicKey(t *testing.T) {
	calls := 0
	base := func(context.Context, sshexec.Target, ...ssh.AuthMethod) (Remote, error) { calls++; return nil, nil }
	if _, err := OwnerBootstrapDialer(base, nil, "not-a-public-key\ncommand")(context.Background(), sshexec.Target{}); err == nil || calls != 0 {
		t.Fatal("invalid key accepted")
	}
}

func TestOwnerBootstrapPreservesKeyWithoutFinalNewline(t *testing.T) {
	r := &bootstrapRemote{}
	calls := 0
	base := func(context.Context, sshexec.Target, ...ssh.AuthMethod) (Remote, error) {
		calls++
		if calls == 1 {
			return nil, sshexec.ErrAuth
		}
		return r, nil
	}
	key := bootstrapKey(t)
	if _, err := OwnerBootstrapDialer(base, ssh.Password("fake"), key)(context.Background(), sshexec.Target{}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "ssh")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "authorized_keys")
	if err := os.WriteFile(file, []byte("existing-owner-key"), 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(r.cmd, "~/.ssh", proberShellQuote(directory))
	if output, err := exec.Command("bash", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "existing-owner-key\n"+key+"\n") {
		t.Fatal("bootstrap joined or overwrote the existing key")
	}
}
