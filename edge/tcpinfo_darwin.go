//go:build darwin

package main

import (
	"time"

	"golang.org/x/sys/unix"
)

// tcpInfo reads the kernel's view of one TCP socket (TCP_CONNECTION_INFO),
// so the e2e tests on a Mac see numbers too. Its counters are in packets and
// bytes where Linux counts segments; for a share of retransmits they agree.
func tcpInfo(fd uintptr) (tcpSample, bool) {
	i, err := unix.GetsockoptTCPConnectionInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_CONNECTION_INFO)
	if err != nil {
		return tcpSample{}, false
	}
	return tcpSample{
		RTT:      time.Duration(i.Srtt) * time.Millisecond,
		RTTVar:   time.Duration(i.Rttvar) * time.Millisecond,
		Cwnd:     int64(i.Snd_cwnd) / max(int64(i.Maxseg), 1),
		SegsOut:  int64(i.Txpackets),
		Retrans:  int64(i.Txretransmitpackets),
		BytesOut: int64(i.Txbytes),
		BytesIn:  int64(i.Rxbytes),
	}, true
}
