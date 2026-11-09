package main

import (
	"bufio"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// VPS is the box itself over one second: whether it, not the path, is what
// is bad. Counters of the interfaces are since boot; the hub takes their
// differences between rounds.
type VPS struct {
	CPUs       int     `json:"cpus"`
	StealPct   float64 `json:"steal_pct"`
	IOWaitPct  float64 `json:"iowait_pct"`
	BusyPct    float64 `json:"busy_pct"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	MemTotalMB int64   `json:"mem_total_mb"`
	MemAvailMB int64   `json:"mem_avail_mb"`
	// Iface is the interface of the default route.
	Iface        string `json:"iface"`
	RxBytes      int64  `json:"rx_bytes"`
	RxPackets    int64  `json:"rx_packets"`
	TxPackets    int64  `json:"tx_packets"`
	TxBytes      int64  `json:"tx_bytes"`
	RxErrs       int64  `json:"rx_errs"`
	RxDrop       int64  `json:"rx_drop"`
	TxErrs       int64  `json:"tx_errs"`
	TxDrop       int64  `json:"tx_drop"`
	Conntrack    int64  `json:"conntrack"`
	ConntrackMax int64  `json:"conntrack_max"`
	// TCP sockets by state (/proc/net/tcp and tcp6).
	TCPEstab    int `json:"tcp_estab"`
	TCPTimeWait int `json:"tcp_timewait"`
	TCPAll      int `json:"tcp_all"`
	// TCP retransmitted segments since boot (/proc/net/snmp).
	RetransSegs int64 `json:"retrans_segs"`
	OutSegs     int64 `json:"out_segs"`
	UptimeSec   int64 `json:"uptime_sec"`
}

func measureVPS() (VPS, error) {
	v := VPS{CPUs: runtime.NumCPU()}
	a, err := cpuTimes()
	if err != nil {
		return v, err
	}
	time.Sleep(time.Second)
	b, err := cpuTimes()
	if err != nil {
		return v, err
	}
	var total float64
	for i := range b {
		total += b[i] - a[i]
	}
	if total > 0 {
		// user nice system idle iowait irq softirq steal
		v.StealPct = round1(100 * (b[7] - a[7]) / total)
		v.IOWaitPct = round1(100 * (b[4] - a[4]) / total)
		v.BusyPct = round1(100 * (total - (b[3] - a[3]) - (b[4] - a[4]) - (b[7] - a[7])) / total)
	}
	if f := fields("/proc/loadavg"); len(f) >= 2 {
		v.Load1, _ = strconv.ParseFloat(f[0], 64)
		v.Load5, _ = strconv.ParseFloat(f[1], 64)
	}
	mem := keyed("/proc/meminfo")
	v.MemTotalMB, v.MemAvailMB = mem["MemTotal"]/1024, mem["MemAvailable"]/1024
	v.Iface = defaultIface()
	if v.Iface != "" {
		if c, ok := ifaceCounters(v.Iface); ok {
			v.RxBytes, v.RxPackets, v.RxErrs, v.RxDrop = c[0], c[1], c[2], c[3]
			v.TxBytes, v.TxPackets, v.TxErrs, v.TxDrop = c[8], c[9], c[10], c[11]
		}
	}
	v.Conntrack = readInt("/proc/sys/net/netfilter/nf_conntrack_count")
	v.ConntrackMax = readInt("/proc/sys/net/netfilter/nf_conntrack_max")
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		e, tw, all := tcpStates(p)
		v.TCPEstab += e
		v.TCPTimeWait += tw
		v.TCPAll += all
	}
	v.RetransSegs, v.OutSegs = tcpSNMP()
	if f := fields("/proc/uptime"); len(f) > 0 {
		up, _ := strconv.ParseFloat(f[0], 64)
		v.UptimeSec = int64(up)
	}
	return v, nil
}

func round1(x float64) float64 { return float64(int64(x*10+0.5)) / 10 }

// cpuTimes is the first line of /proc/stat: user nice system idle iowait
// irq softirq steal.
func cpuTimes() ([8]float64, error) {
	var out [8]float64
	f, err := os.Open("/proc/stat")
	if err != nil {
		return out, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		return out, errors.New("/proc/stat is empty")
	}
	parts := strings.Fields(s.Text())
	if len(parts) < 9 || parts[0] != "cpu" {
		return out, errors.New("/proc/stat: no cpu line")
	}
	for i := 0; i < 8; i++ {
		out[i], _ = strconv.ParseFloat(parts[i+1], 64)
	}
	return out, nil
}

func fields(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

func readInt(path string) int64 {
	f := fields(path)
	if len(f) == 0 {
		return -1
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// keyed reads "Key: value kB" lines.
func keyed(path string) map[string]int64 {
	out := map[string]int64{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) > 0 {
			out[strings.TrimSpace(k)], _ = strconv.ParseInt(f[0], 10, 64)
		}
	}
	return out
}

// defaultIface is the interface of the IPv4 default route.
func defaultIface() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

// ifaceCounters are the 16 counters of iface in /proc/net/dev.
func ifaceCounters(iface string) ([16]int64, bool) {
	var out [16]int64
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return out, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != iface {
			continue
		}
		f := strings.Fields(rest)
		for i := 0; i < 16 && i < len(f); i++ {
			out[i], _ = strconv.ParseInt(f[i], 10, 64)
		}
		return out, true
	}
	return out, false
}

// tcpStates counts sockets of /proc/net/tcp*: established (01), time-wait
// (06) and all.
func tcpStates(path string) (estab, timewait, all int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		all++
		switch f[3] {
		case "01":
			estab++
		case "06":
			timewait++
		}
	}
	return
}

// tcpSNMP reads RetransSegs and OutSegs of the Tcp lines in /proc/net/snmp.
func tcpSNMP() (retrans, out int64) {
	b, err := os.ReadFile("/proc/net/snmp")
	if err != nil {
		return -1, -1
	}
	var head []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "Tcp:" {
			continue
		}
		if head == nil {
			head = f
			continue
		}
		for i := 1; i < len(f) && i < len(head); i++ {
			n, _ := strconv.ParseInt(f[i], 10, 64)
			switch head[i] {
			case "RetransSegs":
				retrans = n
			case "OutSegs":
				out = n
			}
		}
		break
	}
	return
}
