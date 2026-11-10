//go:build linux

package main

import (
	"time"

	"golang.org/x/sys/unix"
)

// tcpInfo reads the kernel's view of one TCP socket (TCP_INFO).
func tcpInfo(fd uintptr) (tcpSample, bool) {
	i, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	if err != nil {
		return tcpSample{}, false
	}
	return tcpSample{
		RTT:      time.Duration(i.Rtt) * time.Microsecond,
		RTTVar:   time.Duration(i.Rttvar) * time.Microsecond,
		Cwnd:     int64(i.Snd_cwnd),
		SegsOut:  int64(i.Segs_out),
		Retrans:  int64(i.Total_retrans),
		BytesOut: int64(i.Bytes_acked),
		BytesIn:  int64(i.Bytes_received),
	}, true
}
