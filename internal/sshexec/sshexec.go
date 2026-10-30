// Package sshexec runs commands on a remote machine over ssh — the transport
// the hub provisions relay boxes with (roadmap 42 П6.15). It knows nothing
// about relays: dial, run a command with stdin, close.
//
// Host keys are trusted on first use: the caller passes the key it recorded
// for the machine, or "" the first time and stores what Dial returns. A key
// that differs from the recorded one is refused — a reinstalled machine or
// someone in the middle — and the caller decides, not this package. Whether a
// "" may be passed at all is the caller's call too: trust on first use is a
// decision about who is logging in and why, which this package cannot see.
//
// Errors name the machine (Target.Name), never its address, and never carry
// what the remote side printed: they end up in the hub's log, and the address
// is the secret the hub keeps sealed (audit 42 С3). Output is returned to the
// caller as a value, for the caller to read and not to log.
package sshexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyChanged: the machine answered with a host key other than the one
// on record.
var ErrHostKeyChanged = errors.New("sshexec: host key differs from the one on record")

// ErrAuth: neither the key nor the password was accepted.
var ErrAuth = errors.New("sshexec: authentication refused")

// Target is where and as whom to log in.
type Target struct {
	Addr string // host:port
	// Name is what errors call the machine: the hub's box name. The address
	// never appears in an error of this package.
	Name string
	User string
	// HostKey is the recorded key in authorized_keys form; "" trusts whatever
	// the machine presents and returns it.
	HostKey string
}

// Client is one ssh connection.
type Client struct {
	c       *ssh.Client
	hostKey string
	name    string
	host    string
}

// Dial logs in with the first auth method the machine accepts.
func Dial(ctx context.Context, t Target, auth ...ssh.AuthMethod) (*Client, error) {
	var seen string
	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			if t.HostKey != "" && seen != strings.TrimSpace(t.HostKey) {
				return ErrHostKeyChanged
			}
			return nil
		},
		Timeout: 15 * time.Second,
	}
	name := t.Name
	if name == "" {
		name = "machine"
	}
	host, _, err := net.SplitHostPort(t.Addr)
	if err != nil {
		host = t.Addr
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return nil, Redact(fmt.Errorf("sshexec: dial %s: %w", name, err), host, name)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, t.Addr, cfg)
	if err != nil {
		conn.Close()
		switch {
		case errors.Is(err, ErrHostKeyChanged):
			return nil, ErrHostKeyChanged
		case strings.Contains(err.Error(), "unable to authenticate"):
			return nil, ErrAuth
		}
		return nil, Redact(fmt.Errorf("sshexec: handshake with %s: %w", name, err), host, name)
	}
	_ = conn.SetDeadline(time.Time{})
	return &Client{c: ssh.NewClient(sc, chans, reqs), hostKey: seen, name: name, host: host}, nil
}

// HostKey is the key the machine presented, in authorized_keys form.
func (c *Client) HostKey() string { return c.hostKey }

// Run runs cmd through the remote shell with stdin, and returns combined
// stdout+stderr. A non-zero exit is an error carrying the exit status only:
// the output is the box's own words, returned as the value and never put
// into an error that a log would keep.
func (c *Client) Run(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	s, err := c.c.NewSession()
	if err != nil {
		return "", Redact(fmt.Errorf("sshexec: session on %s: %w", c.name, err), c.host, c.name)
	}
	defer s.Close()
	// stdout and stderr are copied by two goroutines; one unguarded buffer
	// under both loses output (bytes.Buffer resets itself mid-write).
	out := &lockedBuffer{}
	s.Stdout, s.Stderr = out, out
	if stdin != nil {
		s.Stdin = stdin
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(cmd) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = s.Signal(ssh.SIGKILL)
		_ = s.Close()
		return out.String(), ctx.Err()
	}
	if err != nil {
		return out.String(), c.runError(err)
	}
	return out.String(), nil
}

// runError says a command failed without repeating anything the remote side
// said: an exit status is a number, while the remote's own exit message (and
// any transport error under it) is text the hub does not control.
func (c *Client) runError(err error) error {
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return &ExitError{Machine: c.name, Status: exit.ExitStatus()}
	}
	return Redact(fmt.Errorf("sshexec: run on %s: %w", c.name, err), c.host, c.name)
}

// ExitError is a remote command that ran and exited non-zero.
type ExitError struct {
	Machine string
	Status  int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("sshexec: command on %s exited with status %d", e.Machine, e.Status)
}

// Redact rewrites err's text with every mention of host replaced by name, and
// keeps err itself in the chain, so errors.Is and errors.As still see through
// it. For errors from below this package (net, x/crypto/ssh) whose text quotes
// the address they dialled; and for a caller that holds a Dialer of its own.
//
// Those errors print an IP the way net does, which need not be the way it was
// typed: "2001:DB8:0:0::1" dials and fails as "2001:db8::1" (audit 42 recheck
// НН5). Both spellings are replaced.
func Redact(err error, host, name string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if host != "" {
		msg = strings.ReplaceAll(msg, host, name)
		if ip := net.ParseIP(host); ip != nil {
			msg = strings.ReplaceAll(msg, ip.String(), name)
		}
	}
	return &redacted{msg: msg, err: err}
}

type redacted struct {
	msg string
	err error
}

func (r *redacted) Error() string { return r.msg }
func (r *redacted) Unwrap() error { return r.err }

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Close ends the connection.
func (c *Client) Close() error { return c.c.Close() }
