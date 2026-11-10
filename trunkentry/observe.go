package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

// A tester's stream or flow, as the entry saw it (hikkari-client
// plans/2026-11-10-hikkari-observability-plan.md, этап 1б). The exit marks a
// stream only for a tester's key and tells the entry the observation's id,
// never the key; the entry then writes this line at the mark and at the end.
// It carries the trunk's counters for the stream and the client's own TCP as
// the kernel sees it (the leg client → entry), and nothing that names the
// client or where it goes. Streams nobody marked write nothing.
//
// The hub reads the line from the journal (hikkari-control
// internal/cascadewatch) and joins it to the exit's report by id: its keys are
// that reader's contract.

func logObserved(r trunk.Report) {
	tcp, ok := clientTCP(r.Local)
	log.Print("nekko-trunk-entry: " + observedLine(r, tcp, ok))
}

// tcpSample is the kernel's view of the client's TCP socket.
type tcpSample struct {
	RTT, RTTVar           time.Duration
	Retrans, Lost, Unack  int64
	Cwnd, SegsOut, SegsIn int64
}

func clientTCP(c net.Conn) (tcpSample, bool) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return tcpSample{}, false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return tcpSample{}, false
	}
	var s tcpSample
	var got bool
	_ = raw.Control(func(fd uintptr) { s, got = tcpInfo(fd) })
	return s, got
}

func observedLine(r trunk.Report, tcp tcpSample, haveTCP bool) string {
	var id [8]byte
	for i := 0; i < 8; i++ {
		id[i] = byte(r.ID >> (56 - 8*i))
	}
	end := r.End
	if end == "" {
		end = "-"
	}
	line := fmt.Sprintf("observed id=%s kind=%s phase=%s port=%d age_ms=%d up=%d down=%d chunks=%d resent=%d carriers=%d deaths=%d max_wait_ms=%d dgrams=%d/%d copies=%d end=%s",
		hex.EncodeToString(id[:]), r.Kind, r.Phase, r.Port, r.Age.Milliseconds(), r.Up, r.Down, r.Chunks, r.Resent,
		r.Carriers, r.Deaths, r.MaxWait.Milliseconds(), r.DgramsOut, r.DgramsIn, r.DgramsCopy, end)
	if haveTCP {
		line += fmt.Sprintf(" client_rtt_ms=%.1f client_rttvar_ms=%.1f client_retrans=%d client_lost=%d client_unacked=%d client_cwnd=%d client_segs=%d/%d",
			float64(tcp.RTT.Microseconds())/1000, float64(tcp.RTTVar.Microseconds())/1000, tcp.Retrans, tcp.Lost, tcp.Unack,
			tcp.Cwnd, tcp.SegsOut, tcp.SegsIn)
	}
	return line
}
