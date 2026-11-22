package hoptrace

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const maxSamplesPerSecond = 512

func socketFilter(c Config) ([]unix.SockFilter, error) {
	n := len(c.Peers)
	program := []bpf.Instruction{
		bpf.LoadAbsolute{Off: 12, Size: 2},
		bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: unix.ETH_P_IP, SkipTrue: uint8(4 + 2*n)},
		bpf.LoadAbsolute{Off: 23, Size: 1},
		bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: unix.IPPROTO_UDP, SkipTrue: uint8(2 + 2*n)},
	}
	for group, off := range []uint32{26, 30} {
		program = append(program, bpf.LoadAbsolute{Off: off, Size: 4})
		for i, peer := range c.Peers {
			ip := peer.IP.As4()
			skip := (1-group)*(n+1) + n - i
			program = append(program, bpf.JumpIf{Cond: bpf.JumpEqual, Val: binary.BigEndian.Uint32(ip[:]), SkipTrue: uint8(skip)})
		}
	}
	program = append(program, bpf.RetConstant{Val: 0}, bpf.LoadMemShift{Off: 14})
	n = len(c.Ports)
	for group, off := range []uint32{14, 16} {
		program = append(program, bpf.LoadIndirect{Off: off, Size: 2})
		for i, port := range c.Ports {
			skip := (1-group)*(n+1) + n - i
			program = append(program, bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(port), SkipTrue: uint8(skip)})
		}
	}
	program = append(program, bpf.RetConstant{Val: 0}, bpf.RetConstant{Val: snapLength})
	raw, err := bpf.Assemble(program)
	if err != nil {
		return nil, err
	}
	out := make([]unix.SockFilter, len(raw))
	for i, ins := range raw {
		out[i] = unix.SockFilter{Code: ins.Op, Jt: ins.Jt, Jf: ins.Jf, K: ins.K}
	}
	return out, nil
}

func openCapture(c Config) (int, error) {
	// ETH_P_ALL is needed for the kernel to deliver outgoing frames too.
	var all [2]byte
	binary.BigEndian.PutUint16(all[:], unix.ETH_P_ALL)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(binary.NativeEndian.Uint16(all[:])))
	if err != nil {
		return -1, err
	}
	filter, err := socketFilter(c)
	if err == nil {
		prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
		err = unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &prog)
	}
	if err == nil {
		err = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 100_000})
	}
	if err == nil {
		err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1)
	}
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func Capture(ctx context.Context, config string) error {
	c, err := Decode(config)
	if err != nil {
		return err
	}
	fd, err := openCapture(c)
	if err != nil {
		return fmt.Errorf("open hop trace capture: %w", err)
	}
	defer unix.Close(fd)
	dir := Dir(c.Key)
	log.Printf("hop trace started: %d peers, %d ports", len(c.Peers), len(c.Ports))
	var frame [snapLength]byte
	var control [64]byte
	bucket := Bucket{Second: time.Now().Unix(), Packets: make([]Packet, 0, maxSamplesPerSecond)}
	for ctx.Err() == nil {
		n, oob, _, addr, readErr := unix.Recvmsg(fd, frame[:], control[:], 0)
		now := time.Now()
		if now.Unix() != bucket.Second {
			stats, err := unix.GetsockoptTpacketStats(fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)
			if err != nil || now.Unix() != bucket.Second+1 {
				bucket.Complete = false
			} else {
				bucket.Drops = stats.Drops
			}
			if err := Write(dir, config, bucket); err != nil {
				return err
			}
			lease, err := os.Stat(filepath.Join(dir, "lease"))
			if err != nil || now.Sub(lease.ModTime()) > Lease {
				log.Print("hop trace lease expired")
				return nil
			}
			bucket = Bucket{Second: now.Unix(), Complete: true, Packets: bucket.Packets[:0]}
		}
		if errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EINTR) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		from, ok := addr.(*unix.SockaddrLinklayer)
		if !ok || from.Hatype != unix.ARPHRD_ETHER {
			continue
		}
		p, ok := packet(frame[:n], from.Pkttype == unix.PACKET_OUTGOING, c)
		if !ok {
			continue
		}
		if len(bucket.Packets) == cap(bucket.Packets) {
			bucket.Overflow = true
			continue
		}
		p.At = timestamp(control[:oob])
		if p.At == 0 {
			bucket.Complete = false
			continue
		}
		bucket.Packets = append(bucket.Packets, p)
	}
	return nil
}

func timestamp(control []byte) int64 {
	for len(control) >= unix.CmsgLen(0) {
		header, data, rest, err := unix.ParseOneSocketControlMessage(control)
		if err != nil {
			return 0
		}
		// The shipped architectures (amd64 and arm64) both use two 64-bit
		// values for the kernel's nanosecond timestamp.
		if header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_TIMESTAMPNS && len(data) == 16 {
			return int64(binary.NativeEndian.Uint64(data[:8]))*1000 + int64(binary.NativeEndian.Uint64(data[8:]))/1e6
		}
		control = rest
	}
	return 0
}
