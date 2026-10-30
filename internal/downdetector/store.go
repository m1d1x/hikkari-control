package downdetector

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// StatusWindows is how many 5-minute buckets the verdict looks at (15 min).
const StatusWindows = 3

// maxSubmitsPerWindow caps how often one install may send (and so replace)
// its report for the same window. One counts; the rest are retries.
const maxSubmitsPerWindow = 3

// ErrTooManySubmits means the install already sent this window too often.
var ErrTooManySubmits = errors.New("too many reports for this window")

// cellKey is one aggregation cell of a 5-minute bucket.
type cellKey struct {
	Server  string
	Proto   string
	Src     string
	ASN     uint32
	Country string
	Region  string
}

// contrib is one install's share of a cell. It is kept per install only so
// that a repeated report for the same window replaces the earlier one and
// distinct installs can be counted; it never leaves memory.
type contrib struct {
	OK, Fail, Rtt int
}

type bucket struct {
	cells map[cellKey]map[string]contrib
	// byInstall indexes the cells an install contributed to, for replacement.
	byInstall map[string][]cellKey
	submits   map[string]int
}

func newBucket() *bucket {
	return &bucket{
		cells:     map[cellKey]map[string]contrib{},
		byInstall: map[string][]cellKey{},
		submits:   map[string]int{},
	}
}

func (b *bucket) remove(install string) {
	for _, k := range b.byInstall[install] {
		delete(b.cells[k], install)
		if len(b.cells[k]) == 0 {
			delete(b.cells, k)
		}
	}
	delete(b.byInstall, install)
}

// Store holds the in-memory 5-minute buckets. Buckets older than the status
// range stay only until their hour is rolled up.
type Store struct {
	mu      sync.Mutex
	buckets map[int64]*bucket // window start → bucket
}

func NewStore() *Store { return &Store{buckets: map[int64]*bucket{}} }

// Add folds a validated report into its bucket. A second report from the
// same install for the same window replaces the first.
func (s *Store) Add(r Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[r.Window[0]]
	if b == nil {
		b = newBucket()
		s.buckets[r.Window[0]] = b
	}
	if b.submits[r.Install] >= maxSubmitsPerWindow {
		return ErrTooManySubmits
	}
	b.submits[r.Install]++
	b.remove(r.Install)
	keys := make([]cellKey, 0, len(r.Servers))
	for _, st := range r.Servers {
		k := cellKey{st.ID, st.Proto, st.Src, r.ASN, r.Country, r.Region}
		c := b.cells[k]
		if c == nil {
			c = map[string]contrib{}
			b.cells[k] = c
		}
		c[r.Install] = contrib{st.OK, st.Fail, st.RttP50}
		keys = append(keys, k)
	}
	b.byInstall[r.Install] = keys
	return nil
}

// Group is the tunnel-probe summary of one user group over the status range.
type Group struct {
	Installs int `json:"installs"`
	OK       int `json:"ok"`
	Fail     int `json:"fail"`
}

// Groups splits the last StatusWindows buckets of server id, src=tunnel
// only, into the user's own ASN, the rest of their country, and abroad.
func (s *Store) Groups(id string, asn uint32, country string, now time.Time) (sameASN, sameCountry, abroad Group) {
	type acc struct {
		installs map[string]bool
		ok, fail int
	}
	var groups [3]acc
	for i := range groups {
		groups[i].installs = map[string]bool{}
	}
	cur := alignedStart(now)
	s.mu.Lock()
	for start, b := range s.buckets {
		if start >= cur || start < cur-StatusWindows*WindowSec {
			continue
		}
		for k, c := range b.cells {
			if k.Server != id || k.Src != SrcTunnel {
				continue
			}
			g := -1
			switch {
			case k.Country == "" || country == "":
			case k.Country != country:
				g = 2
			case asn != 0 && k.ASN == asn:
				g = 0
			default:
				g = 1
			}
			if g < 0 {
				continue
			}
			for inst, v := range c {
				groups[g].installs[inst] = true
				groups[g].ok += v.OK
				groups[g].fail += v.Fail
			}
		}
	}
	s.mu.Unlock()
	out := func(a acc) Group { return Group{len(a.installs), a.ok, a.fail} }
	return out(groups[0]), out(groups[1]), out(groups[2])
}

// HourCells is one hour of buckets handed to the rollup.
type HourCells map[cellKey]map[string]contrib

// TakeHours removes and returns every complete hour that can no longer get
// reports (all of it older than MaxReportAge), or every hour at all when
// all is set (shutdown). Buckets inside the status range are never taken
// unless all is set.
func (s *Store) TakeHours(now time.Time, all bool) map[int64]HourCells {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int64]HourCells{}
	for start, b := range s.buckets {
		hour := start - start%3600
		if !all && time.Unix(hour+3600, 0).Add(MaxReportAge).After(now) {
			continue
		}
		hc := out[hour]
		if hc == nil {
			hc = HourCells{}
			out[hour] = hc
		}
		for k, c := range b.cells {
			m := hc[k]
			if m == nil {
				m = map[string]contrib{}
				hc[k] = m
			}
			for inst, v := range c {
				p := m[inst]
				m[inst] = mergeContrib(p, v)
			}
		}
		delete(s.buckets, start)
	}
	return out
}

// mergeContrib adds one install's windows within an hour. Rtt becomes the
// sample-weighted mean of the windows' medians.
func mergeContrib(a, b contrib) contrib {
	rtt := b.Rtt
	if a.OK+b.OK > 0 {
		rtt = (a.Rtt*a.OK + b.Rtt*b.OK) / (a.OK + b.OK)
	}
	return contrib{a.OK + b.OK, a.Fail + b.Fail, rtt}
}

func alignedStart(now time.Time) int64 {
	t := now.Unix()
	return t - t%WindowSec
}

// RollupRow is one line of rollup_hourly.
type RollupRow struct {
	Hour      int64
	Server    string
	Proto     string
	Src       string
	ASN       uint32
	Country   string
	Region    string
	OK        int
	Fail      int
	RttP50Avg int
	Installs  int
}

// Rollup turns one hour of cells into rows safe to store:
//   - a server only one install reported this hour is dropped (most likely
//     the user's own server);
//   - an ASN with fewer than k installs (per server/proto/src/country) folds
//     into asn=0, then a region with fewer than k into "other";
//   - rows still below k after folding are dropped.
func Rollup(hour int64, cells HourCells, k int) []RollupRow {
	if k < 1 {
		k = 1
	}
	perServer := map[string]map[string]bool{}
	for key, c := range cells {
		addInstalls(perServer, key.Server, c)
	}

	type asnKey struct {
		Server, Proto, Src, Country string
		ASN                         uint32
	}
	asnInst := map[asnKey]map[string]bool{}
	for key, c := range cells {
		if len(perServer[key.Server]) < 2 {
			continue
		}
		addInstalls(asnInst, asnKey{key.Server, key.Proto, key.Src, key.Country, key.ASN}, c)
	}
	foldASN := func(key cellKey) cellKey {
		if len(asnInst[asnKey{key.Server, key.Proto, key.Src, key.Country, key.ASN}]) < k {
			key.ASN = 0
		}
		return key
	}

	regInst := map[cellKey]map[string]bool{}
	for key, c := range cells {
		if len(perServer[key.Server]) < 2 {
			continue
		}
		addInstalls(regInst, foldASN(key), c)
	}

	type acc struct {
		installs           map[string]bool
		ok, fail, rtt, rtN int
	}
	rows := map[cellKey]*acc{}
	for key, c := range cells {
		if len(perServer[key.Server]) < 2 {
			continue
		}
		fk := foldASN(key)
		if len(regInst[fk]) < k {
			fk.Region = "other"
		}
		a := rows[fk]
		if a == nil {
			a = &acc{installs: map[string]bool{}}
			rows[fk] = a
		}
		for inst, v := range c {
			a.installs[inst] = true
			a.ok += v.OK
			a.fail += v.Fail
			if v.OK > 0 {
				a.rtt += v.Rtt
				a.rtN++
			}
		}
	}

	out := make([]RollupRow, 0, len(rows))
	for key, a := range rows {
		if len(a.installs) < k {
			continue
		}
		avg := 0
		if a.rtN > 0 {
			avg = a.rtt / a.rtN
		}
		out = append(out, RollupRow{hour, key.Server, key.Proto, key.Src, key.ASN, key.Country, key.Region,
			a.ok, a.fail, avg, len(a.installs)})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		if a.Src != b.Src {
			return a.Src < b.Src
		}
		if a.Country != b.Country {
			return a.Country < b.Country
		}
		if a.ASN != b.ASN {
			return a.ASN < b.ASN
		}
		return a.Region < b.Region
	})
	return out
}

func addInstalls[K comparable](m map[K]map[string]bool, key K, c map[string]contrib) {
	set := m[key]
	if set == nil {
		set = map[string]bool{}
		m[key] = set
	}
	for inst := range c {
		set[inst] = true
	}
}
