package hoptrace

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.org/x/net/bpf"
)

func TestFingerprintSurvivesNATAndCaptureSelectsPeers(t *testing.T) {
	entry := netip.MustParseAddr("192.0.2.1")
	point := netip.MustParseAddr("198.51.100.1")
	core := netip.MustParseAddr("203.0.113.1")
	c := Config{Peers: []Peer{{"point", point}, {"core", core}}, Ports: []uint16{28461}}
	before := udpFrame(entry, point, 40001, 28461)
	after := udpFrame(point, core, 40002, 28461)
	var a, b Packet
	var ok bool
	for i := range 256 {
		before[42], after[42] = byte(i), byte(i)
		a, ok = packet(before, true, c)
		if ok {
			b, ok = packet(after, false, c)
			break
		}
	}
	if !ok || a.ID != b.ID || a.Port != b.Port || !a.Out || b.Out {
		t.Fatalf("fingerprint before and after NAT: %+v %+v", a, b)
	}
	filter, err := socketFilter(c)
	if err != nil {
		t.Fatal(err)
	}
	var raw []bpf.RawInstruction
	for _, ins := range filter {
		raw = append(raw, bpf.RawInstruction{Op: ins.Code, Jt: ins.Jt, Jf: ins.Jf, K: ins.K})
	}
	program, all := bpf.Disassemble(raw)
	vm, err := bpf.NewVM(program)
	if err != nil || !all {
		t.Fatalf("filter: %t %v", all, err)
	}
	check := func(frame []byte, want int) {
		t.Helper()
		if got, err := vm.Run(frame); err != nil || got != want {
			t.Fatalf("filter = %d, %v; want %d", got, err, want)
		}
	}
	check(before, snapLength)
	check(after, snapLength)
	check(udpFrame(entry, entry, 40001, 28461), 0)
	check(udpFrame(entry, point, 40001, 12345), 0)
	check(udpFrame(core, point, 28461, 40001), snapLength)
	before[23] = 6
	check(before, 0)
	before[23] = 17
	before[13] = 1
	check(before, 0)
}

func udpFrame(src, dst netip.Addr, sourcePort, destPort uint16) []byte {
	frame := make([]byte, 80)
	frame[12], frame[13], frame[14], frame[23] = 8, 0, 0x45, 17
	copy(frame[26:30], src.AsSlice())
	copy(frame[30:34], dst.AsSlice())
	binary.BigEndian.PutUint16(frame[34:36], sourcePort)
	binary.BigEndian.PutUint16(frame[36:38], destPort)
	binary.BigEndian.PutUint16(frame[38:40], uint16(len(frame)-34))
	return frame
}
