package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func tcpSink(t *testing.T, port, n int) {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				got, _ := io.CopyN(io.Discard, c, int64(n))
				fmt.Fprintf(c, "%d\n", got)
			}()
		}
	}()
}

func TestZZUpload(t *testing.T) {
	const n = 1 << 20
	edgePort, local := freePort(t, "tcp"), freeLocalPortBase(t)
	sinkPort := freePort(t, "tcp")
	tcpSink(t, sinkPort, n)
	m, pin := xhttpEnv(t, edgePort, local)
	_, _ = startXhttpEdge(t, m, sinkPort)
	for _, mode := range []string{"packet-up", "stream-up", "stream-one", "auto"} {
		t.Run(mode, func(t *testing.T) {
			doors := xhttpClient(t, edgePort, pin, mode, map[string]int{"sink": sinkPort})
			c, err := net.Dial("tcp4", "127.0.0.1:"+strconv.Itoa(doors["sink"]))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			buf := make([]byte, n)
			_, _ = rand.Read(buf)
			start := time.Now()
			if _, err := c.Write(buf); err != nil {
				t.Fatalf("write: %v", err)
			}
			reply := make([]byte, 32)
			k, err := c.Read(reply)
			t.Logf("%s: reply %q err %v in %v", mode, reply[:k], err, time.Since(start))
		})
	}
}
