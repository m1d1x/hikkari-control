package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A cascade edge (EDGE_PROTO=cascade) is the Russian half of the РФ-каскад
// (plans 2026-11-01-rf-censorship-bypass-research.md §2.4): a box inside
// Russia that a client reaches without crossing the border, which hands every
// TCP connection, byte for byte, to an XHTTP exit abroad.
//
//	client ─TLS (exit's domain)─► :443 cascade (RU) ─TCP─► exit:443 ─► internet
//
// It terminates nothing. TLS, the certificate and its renewals (TLS-ALPN-01
// rides the same pipe), the secret path and the VLESS users all stay on the
// exit; the cascade holds no credential and runs no Xray. A censor who takes
// the box learns one exit address, which the hub already treats as burnable.
//
// The copy is Go's io.Copy between two *net.TCPConn, which Linux turns into
// splice(2): the bytes do not pass through user space.

// protoCascade is EDGE_PROTO for a cascade edge. It is not a relay protocol:
// the hub signs no record for it, and a client never sees the word.
const protoCascade = "cascade"

// CascadeConfig is everything one cascade edge needs, read from the
// environment.
type CascadeConfig struct {
	// Forwards maps each public port to the exit address it is handed to.
	Forwards map[int]string
	// DialTimeout bounds the connection to the exit.
	DialTimeout time.Duration
}

// cascadeDialTimeout is how long a client waits for the exit before its
// connection is dropped: longer than any healthy cross-border handshake, short
// enough that a dead exit frees the client to try its next relay.
const cascadeDialTimeout = 10 * time.Second

// CascadeConfigFromEnv reads EDGE_FORWARD: comma-separated port=host:port,
// e.g. "443=203.0.113.8:443". Every problem is an error before anything
// listens.
func CascadeConfigFromEnv(getenv func(string) string) (CascadeConfig, error) {
	c := CascadeConfig{Forwards: map[int]string{}, DialTimeout: cascadeDialTimeout}
	raw := strings.TrimSpace(getenv("EDGE_FORWARD"))
	if raw == "" {
		return c, errors.New("EDGE_FORWARD: empty, a cascade must forward somewhere")
	}
	for _, item := range strings.Split(raw, ",") {
		port, to, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			return c, fmt.Errorf("EDGE_FORWARD: %q is not port=host:port", item)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return c, fmt.Errorf("EDGE_FORWARD: port %q", port)
		}
		host, tp, err := net.SplitHostPort(to)
		if err != nil || net.ParseIP(host) == nil {
			return c, fmt.Errorf("EDGE_FORWARD: %q is not ip:port", to)
		}
		if n, err := strconv.Atoi(tp); err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("EDGE_FORWARD: target port %q", tp)
		}
		if _, dup := c.Forwards[p]; dup {
			return c, fmt.Errorf("EDGE_FORWARD: port %d twice", p)
		}
		c.Forwards[p] = to
	}
	return c, nil
}

// runCascade listens on every forwarded port until SIGTERM.
func runCascade(ctx context.Context, c CascadeConfig) error {
	ports := make([]int, 0, len(c.Forwards))
	for p := range c.Forwards {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	for _, p := range ports {
		ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(p)))
		if err != nil {
			return fmt.Errorf("cascade :%d: %w", p, err)
		}
		go serveCascade(ctx, ln, c.Forwards[p], c.DialTimeout)
		go func() { <-ctx.Done(); ln.Close() }()
	}
	log.Printf("nekko-edge: cascade up, %d port(s)", len(ports))
	return nil
}

func serveCascade(ctx context.Context, ln net.Listener, to string, timeout time.Duration) {
	dialer := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	for {
		in, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A full descriptor table and the like pass; the listener stays.
			log.Printf("nekko-edge: cascade accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go func() {
			defer in.Close()
			out, err := dialer.DialContext(ctx, "tcp", to)
			if err != nil {
				// Said without the client's address: the edge keeps nothing.
				log.Printf("nekko-edge: cascade: exit unreachable: %v", err)
				return
			}
			defer out.Close()
			pipe(in, out)
		}()
	}
}

// pipe copies both ways until both directions end. A side that finishes
// sending half-closes the other's write, so a client's end of request reaches
// the exit without tearing down the answer on its way back.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	one := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if c, ok := dst.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go one(a, b)
	go one(b, a)
	wg.Wait()
}
