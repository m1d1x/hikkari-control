// Command nekko-cascade-probe is the cascade watch's measurement on a box
// (hikkari-control internal/cascadewatch; hikkari-client
// plans/2026-11-05-ru-cascade.md): the hub stages it on the trunk entry, the
// exit and the control vantage, runs it over SSH and reads one JSON line per
// run. Pure Go, no secrets in its arguments or its output.
//
//	nekko-cascade-probe vps                          the box itself: CPU steal, load, memory, interfaces, conntrack, sockets
//	nekko-cascade-probe path <ip:port> [flows]       bare TCP from flows source ports, and ICMP via ping
//	nekko-cascade-probe trunk <single|pool> <host:port> [chunks] [size]
//	                                                 chunks from a provoke receiver (edge/provoke.go), the trunk's layer
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var out any
	var err error
	switch os.Args[1] {
	case "vps":
		out, err = measureVPS()
	case "path":
		if len(os.Args) < 3 {
			usage()
		}
		out, err = measurePath(os.Args[2], intArg(3, defaultFlows))
	case "trunk":
		if len(os.Args) < 4 {
			usage()
		}
		out, err = measureTrunk(os.Args[2], os.Args[3], intArg(4, defaultChunks), int64(intArg(5, defaultChunkSize)))
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nekko-cascade-probe:", err)
		os.Exit(1)
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func intArg(i, def int) int {
	if len(os.Args) <= i {
		return def
	}
	n, err := strconv.Atoi(os.Args[i])
	if err != nil || n <= 0 {
		usage()
	}
	return n
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: nekko-cascade-probe vps | path <ip:port> [flows] | trunk <single|pool> <host:port> [chunks] [size]")
	os.Exit(2)
}
