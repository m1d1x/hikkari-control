package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeSTUN answers binding requests with a success response carrying the same
// transaction id, dropping every drop-th request.
func fakeSTUN(t *testing.T, drop int) int {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		b := make([]byte, 2048)
		n := 0
		for {
			l, from, err := c.ReadFromUDP(b)
			if err != nil || l < 20 {
				return
			}
			n++
			if drop > 0 && n%drop == 0 {
				continue
			}
			resp := make([]byte, 20)
			binary.BigEndian.PutUint16(resp, 0x0101)
			copy(resp[4:], b[4:20])
			_, _ = c.WriteToUDP(resp, from)
		}
	}()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func TestStunRequestCarriesSeq(t *testing.T) {
	nonce := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	b := stunRequest(42, nonce)
	if len(b) != voicePayload || binary.BigEndian.Uint16(b[2:]) != voicePayload-20 {
		t.Fatalf("size %d, length field %d", len(b), binary.BigEndian.Uint16(b[2:]))
	}
	if seq, ok := stunSeq(b, nonce); !ok || seq != 42 {
		t.Fatalf("seq %d ok %v", seq, ok)
	}
	if _, ok := stunSeq(b, [8]byte{}); ok {
		t.Fatal("another burst's answer counted")
	}
}

func TestStunBurstCounts(t *testing.T) {
	p := stunBurst(context.Background(), fakeSTUN(t, 0), time.Second)
	if p.sent != voicePackets || p.got != voicePackets || !p.ok() {
		t.Fatalf("clean path: %+v", p)
	}
	p = stunBurst(context.Background(), fakeSTUN(t, 4), time.Second)
	if p.got >= voicePackets || p.got < voicePackets/2 {
		t.Fatalf("lossy path: %+v", p)
	}
}

func TestStunBurstDeadChannel(t *testing.T) {
	c, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}) // answers nothing
	defer c.Close()
	start := time.Now()
	p := stunBurst(context.Background(), c.LocalAddr().(*net.UDPAddr).Port, time.Second)
	if p.got != 0 || p.ok() || time.Since(start) > datagramWarmup+time.Second {
		t.Fatalf("dead channel: %+v after %v", p, time.Since(start))
	}
}

func TestWithDoorKeepsVerdict(t *testing.T) {
	r := withDoor(probeResult{Verdict: verdictClean, Detail: "naive"}, "datagram", voiceProbe{sent: 100, got: 97})
	if r.Verdict != verdictClean || r.Detail != "naive; datagram 97/100" || r.XUDPGot != 97 || r.XUDPSent != 100 {
		t.Fatalf("%+v", r)
	}
}

// Live: HIKKARI_TEST_CORE (a client core) and HIKKARI_TEST_RECORD (a signed
// record of a Hikkari relay, `hub entry-box record <relay>`).
func TestDatagramDoorLive(t *testing.T) {
	core, recordPath := os.Getenv("HIKKARI_TEST_CORE"), os.Getenv("HIKKARI_TEST_RECORD")
	if core == "" || recordPath == "" {
		t.Skip("HIKKARI_TEST_CORE and HIKKARI_TEST_RECORD not set")
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var env struct{ Payload string }
	_ = json.Unmarshal(raw, &env)
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	var rec struct {
		Address, Host, User, Pass, Name string
		Port                            int
	}
	if err := json.Unmarshal(payload, &rec); err != nil {
		t.Fatal(err)
	}
	e := edge{Name: rec.Name, Proto: protoHikkari, Address: rec.Address, Port: rec.Port, Naive: &naiveEdge{Host: rec.Host, User: rec.User, Pass: rec.Pass}}
	r := probeDatagram(context.Background(), core, e, probeResult{Verdict: verdictClean, Detail: "naive"}, 3*time.Second)
	t.Logf("%s", r.Detail)
	if r.XUDPGot < 90 || !strings.HasPrefix(r.Detail, "naive; datagram ") {
		t.Fatalf("live datagram channel: %+v", r)
	}
}
