package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestTheCascadeForwardsAreReadStrictly(t *testing.T) {
	c, err := CascadeConfigFromEnv(env(map[string]string{"EDGE_FORWARD": "443=203.0.113.8:443, 8443=203.0.113.8:8443"}))
	if err != nil || c.Forwards[443] != "203.0.113.8:443" || c.Forwards[8443] != "203.0.113.8:8443" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{"", "443", "443=exit.example:443", "0=203.0.113.8:443", "443=203.0.113.8", "443=203.0.113.8:443,443=203.0.113.9:443"} {
		if _, err := CascadeConfigFromEnv(env(map[string]string{"EDGE_FORWARD": bad})); err == nil {
			t.Errorf("EDGE_FORWARD=%q accepted", bad)
		}
	}
}

// A request goes through to the exit and the answer comes back after the
// client has finished sending: the half-close does not cut the reply.
func TestTheCascadeCarriesBothWays(t *testing.T) {
	exit, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer exit.Close()
	go func() {
		c, err := exit.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		got, _ := io.ReadAll(c)
		_, _ = c.Write(append([]byte("echo:"), got...))
	}()

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveCascade(ctx, front, exit.Addr().String(), time.Second)
	go func() { <-ctx.Done(); front.Close() }()

	c, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	line, _ := bufio.NewReader(c).ReadString('\n')
	if line != "echo:hello" {
		t.Fatalf("got %q through the cascade", line)
	}
}
