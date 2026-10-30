// Package downdetector is the server side of the shared downdetector
// (hikkari-client docs/DOWNDETECTOR.md). Clients send anonymous 5-minute
// aggregates of their probes; the server sums them in memory, probes known
// servers from the outside and answers why a server does not work for a
// user: server_down, isp_block, country_block, local or unknown.
//
// Privacy rules this package keeps:
//   - a raw report is never stored or logged: it is added to in-memory sums
//     and dropped;
//   - install ids live in memory only, inside the current buckets;
//   - only hourly aggregates with at least k distinct installs reach disk.
package downdetector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// WindowSec is the report period. Client windows are aligned to it.
const WindowSec = 300

const (
	MaxBodyBytes = 64 << 10
	MaxServers   = 200
	MaxReportAge = 2 * time.Hour
	maxClockSkew = 60 * time.Second
	maxSamples   = 10000 // ok+fail per server per window; the client probes every 30 s
	maxRttMs     = 60000
)

// Probe sources. Only SrcTunnel counts for blocking verdicts: a TCP connect
// passes even when DPI cuts the protocol.
const (
	SrcTunnel = "tunnel"
	SrcTCP    = "tcp"
	SrcICMP   = "icmp"
)

// Report mirrors hikkari-client engine/core/downdetector.Report exactly.
type Report struct {
	Install string       `json:"install"`
	ASN     uint32       `json:"asn"`
	Country string       `json:"country"`
	Region  string       `json:"region"`
	Window  [2]int64     `json:"window"`
	Servers []ServerStat `json:"servers"`
}

type ServerStat struct {
	ID     string `json:"id"`
	Proto  string `json:"proto"`
	Src    string `json:"src"`
	OK     int    `json:"ok"`
	Fail   int    `json:"fail"`
	RttP50 int    `json:"rtt_p50"`
}

var (
	reInstall  = regexp.MustCompile(`^r-[0-9a-f]{24}$`)
	reServerID = regexp.MustCompile(`^s:[0-9a-f]{32}$|^n:[a-z0-9-]{1,32}$`)
	reCountry  = regexp.MustCompile(`^[a-z]{2}$`)
	reSlug     = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
)

// ErrInvalid marks a report that must be rejected with 400.
var ErrInvalid = errors.New("invalid report")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// DecodeReport reads and validates one report. Unknown fields are rejected:
// the report schema is a public, closed list.
func DecodeReport(r io.Reader, now time.Time) (Report, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes+1))
	if err != nil {
		return Report{}, invalid("read body")
	}
	if len(body) > MaxBodyBytes {
		return Report{}, invalid("body over %d bytes", MaxBodyBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var rep Report
	if err := dec.Decode(&rep); err != nil {
		return Report{}, invalid("malformed json")
	}
	if dec.More() {
		return Report{}, invalid("trailing data")
	}
	return rep, rep.Validate(now)
}

// Validate checks a report against the schema and the clock.
func (r Report) Validate(now time.Time) error {
	if !reInstall.MatchString(r.Install) {
		return invalid("install")
	}
	if r.Country != "" && !reCountry.MatchString(r.Country) {
		return invalid("country")
	}
	if r.Region != "" && !reSlug.MatchString(r.Region) {
		return invalid("region")
	}
	start, end := r.Window[0], r.Window[1]
	if start%WindowSec != 0 || end-start != WindowSec {
		return invalid("window not aligned to %d s", WindowSec)
	}
	if time.Unix(end, 0).After(now.Add(maxClockSkew)) {
		return invalid("window in the future")
	}
	if time.Unix(start, 0).Before(now.Add(-MaxReportAge)) {
		return invalid("window older than %s", MaxReportAge)
	}
	if len(r.Servers) > MaxServers {
		return invalid("more than %d servers", MaxServers)
	}
	type key struct{ id, src string }
	seen := make(map[key]bool, len(r.Servers))
	for _, s := range r.Servers {
		if !reServerID.MatchString(s.ID) {
			return invalid("server id")
		}
		if !reSlug.MatchString(s.Proto) {
			return invalid("proto")
		}
		switch s.Src {
		case SrcTunnel, SrcTCP, SrcICMP:
		default:
			return invalid("src")
		}
		if s.OK < 0 || s.Fail < 0 || s.OK+s.Fail == 0 || s.OK+s.Fail > maxSamples {
			return invalid("ok/fail")
		}
		if s.RttP50 < 0 || s.RttP50 > maxRttMs {
			return invalid("rtt_p50")
		}
		k := key{s.ID, s.Src}
		if seen[k] {
			return invalid("duplicate server/src")
		}
		seen[k] = true
	}
	return nil
}

// ValidServerID reports whether id has the client's id format.
func ValidServerID(id string) bool { return reServerID.MatchString(id) }
