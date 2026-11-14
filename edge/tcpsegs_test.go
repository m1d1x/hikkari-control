package main

import "testing"

// The box's TCP counters come from the "Tcp:" pair of /proc/net/snmp, the
// value line matched to the name line by position.
func TestTheBoxsTCPSegmentsComeFromTheSnmpTable(t *testing.T) {
	snmp := `Ip: Forwarding DefaultTTL InReceives
Ip: 2 64 3317716
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 5213 81234 112 3021 64 9123456 10400000 52000 3 4100 0
Udp: InDatagrams NoPorts InErrors OutDatagrams
Udp: 10 2 0 11
`
	out, retrans := tcpSegs(snmp)
	if out != 10400000 || retrans != 52000 {
		t.Fatalf("tcpSegs = %d, %d; want 10400000, 52000", out, retrans)
	}
	if out, retrans := tcpSegs("Ip: a\nIp: 1\n"); out != 0 || retrans != 0 {
		t.Fatalf("no Tcp table: %d, %d; want zeroes", out, retrans)
	}
}
