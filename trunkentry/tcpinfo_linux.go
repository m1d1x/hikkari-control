//go:build linux

package main

import (
	"time"

	"golang.org/x/sys/unix"
)

// tcpInfo reads TCP_INFO of one socket.
func tcpInfo(fd uintptr) (tcpSample, bool) {
	i, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	if err != nil {
		return tcpSample{}, false
	}
	return tcpSample{
		RTT: time.Duration(i.Rtt) * time.Microsecond, RTTVar: time.Duration(i.Rttvar) * time.Microsecond,
		Retrans: int64(i.Total_retrans), Lost: int64(i.Lost), Unack: int64(i.Unacked),
		Cwnd: int64(i.Snd_cwnd), SegsOut: int64(i.Segs_out), SegsIn: int64(i.Segs_in),
	}, true
}
