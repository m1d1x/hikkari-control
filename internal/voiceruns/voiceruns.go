// Package voiceruns keeps testers' voice series: what a call would hear through
// the tunnel and from the bare network beside it. The client writes one
// [diag:voice] line per series (hikkari-client
// engine/core/engine/singbox/diagnostics/voice.go): every 30 minutes in tester
// mode, every 2 hours on a mobile network, and on probe_voice. The ingester
// moves the lines from the week-long log stream into voice_runs, kept for good,
// and the ops plane reads them grouped with a verdict.
//
// The question the dataset answers (hikkari-client
// plans/research/2026-11-08-udp-abroad-from-testers.md): does a call through a
// TCP profile (VLESS, Naive: UDP inside a TCP stream) suffer on testers'
// networks? A stream that stalls holds every datagram behind it, so the loss
// stays near zero and the call still hears a pause; the gaps between arrivals
// are what count. The owner's stop rule: silences over 60 ms rarer than one a
// minute on every network, and TCP is enough.
package voiceruns

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Row is one series.
type Row struct {
	ID            int64     `json:"id"`
	InstallID     string    `json:"install_id"`
	Alias         string    `json:"alias,omitempty"`
	MeasuredAt    time.Time `json:"measured_at"`
	ClientVersion string    `json:"client_version,omitempty"`
	OnRequest     bool      `json:"on_request,omitempty"`
	Path          string    `json:"path"`
	Profile       string    `json:"profile,omitempty"`
	Proto         string    `json:"proto,omitempty"`
	Iface         string    `json:"iface,omitempty"`
	Network       string    `json:"network"`
	NetworkAt     string    `json:"network_at,omitempty"`
	Mobile        bool      `json:"mobile"`
	Sent          int       `json:"sent"`
	Recv          int       `json:"recv"`
	LossRun       int       `json:"loss_run"`
	RTTP50        *int      `json:"rtt_p50_ms,omitempty"`
	RTTP95        *int      `json:"rtt_p95_ms,omitempty"`
	RTTMax        *int      `json:"rtt_max_ms,omitempty"`
	Jitter        *float64  `json:"jitter_ms,omitempty"`
	Gaps60        int       `json:"gaps60"`
	Gaps150       int       `json:"gaps150"`
	GapMax        *int      `json:"gap_max_ms,omitempty"`
	Reorder       int       `json:"reorder"`
	Dup           int       `json:"dup"`
	Error         string    `json:"error,omitempty"`

	key string // install | line time | path
}

var (
	reLineTS = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+`)
	// The periodic round writes «[diag:voice] path=…»; probe_voice's answer
	// comes as «[diag:voice nonce=…] path=…».
	reVoice = regexp.MustCompile(`\[diag:voice( nonce=\S+)?\] (path=.*)$`)
	reError = regexp.MustCompile(`error="((?:[^"\\]|\\.)*)"`)
	reToken = regexp.MustCompile(`^[\w.\-:/]{1,64}$`)
)

// parseLine reads one [diag:voice] line of an install's chunk.
func parseLine(line, install string, receivedAt time.Time, version string) (Row, bool) {
	m := reVoice.FindStringSubmatch(line)
	if m == nil {
		return Row{}, false
	}
	rest := m[2]
	r := Row{InstallID: install, MeasuredAt: receivedAt, ClientVersion: version, OnRequest: m[1] != ""}
	if e := reError.FindStringSubmatch(rest); e != nil {
		if s, err := strconv.Unquote(`"` + e[1] + `"`); err == nil {
			r.Error = s
		} else {
			r.Error = e[1]
		}
		rest = strings.Replace(rest, e[0], "", 1)
	}
	f := map[string]string{}
	for _, w := range strings.Fields(rest) {
		k, v, ok := strings.Cut(w, "=")
		if !ok {
			continue
		}
		if _, seen := f[k]; !seen {
			f[k] = v
		}
	}
	r.Path = f["path"]
	if r.Path != "tun" && r.Path != "direct" {
		return Row{}, false
	}
	token := func(k string) string {
		if v := f[k]; reToken.MatchString(v) {
			return v
		}
		return ""
	}
	r.Profile, r.Proto, r.Iface = token("profile"), token("proto"), token("iface")
	r.Network, r.NetworkAt = token("isp"), token("isp_at")
	r.Mobile = f["mobile"] == "1"
	num := func(k string) int { n, _ := strconv.Atoi(f[k]); return n }
	opt := func(k string) *int {
		if n, err := strconv.Atoi(f[k]); err == nil {
			return &n
		}
		return nil
	}
	r.Sent, r.Recv, r.LossRun = num("sent"), num("recv"), num("loss_run")
	r.RTTP50, r.RTTP95, r.RTTMax, r.GapMax = opt("rtt_p50"), opt("rtt_p95"), opt("rtt_max"), opt("gap_max")
	if j, err := strconv.ParseFloat(f["jitter"], 64); err == nil {
		r.Jitter = &j
	}
	r.Gaps60, r.Gaps150, r.Reorder, r.Dup = num("gaps60"), num("gaps150"), num("reorder"), num("dup")
	if r.Sent == 0 && r.Error == "" {
		return Row{}, false // neither a measurement nor a reason there is none
	}
	ts := receivedAt.UTC().Format(time.RFC3339Nano)
	if t := reLineTS.FindStringSubmatch(line); t != nil {
		ts = t[1]
	}
	r.key = install + "|" + ts + "|" + r.Path
	return r, true
}

// Store reads and writes voice_runs.
type Store struct{ pool db.Pool }

// NewStore keeps the dataset in pool.
func NewStore(pool db.Pool) *Store { return &Store{pool: pool} }

// Insert adds rows, skipping those already kept; it returns how many were new.
func (s *Store) Insert(ctx context.Context, rows []Row) (int, error) {
	added := 0
	for _, r := range rows {
		if r.key == "" {
			return added, errors.New("voice row without a source key")
		}
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO voice_runs (source_key, install_id, measured_at, client_version, on_request, path,
			                        profile, proto, iface, network, network_at, mobile, sent, recv, loss_run,
			                        rtt_p50_ms, rtt_p95_ms, rtt_max_ms, jitter_ms, gaps60, gaps150, gap_max_ms,
			                        reorder, dup, error)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
			ON CONFLICT (source_key) DO NOTHING
		`, r.key, r.InstallID, r.MeasuredAt.UTC(), r.ClientVersion, r.OnRequest, r.Path,
			r.Profile, r.Proto, r.Iface, r.Network, r.NetworkAt, r.Mobile, r.Sent, r.Recv, r.LossRun,
			r.RTTP50, r.RTTP95, r.RTTMax, r.Jitter, r.Gaps60, r.Gaps150, r.GapMax,
			r.Reorder, r.Dup, r.Error)
		if err != nil {
			return added, fmt.Errorf("insert voice run: %w", err)
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

// Query picks series.
type Query struct {
	Install string
	Proto   string
	Since   time.Time
}

// Rows reads a query's series, oldest first.
func (s *Store) Rows(ctx context.Context, q Query) ([]Row, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, install_id, measured_at, client_version, on_request, path, profile, proto, iface,
		       network, network_at, mobile, sent, recv, loss_run, rtt_p50_ms, rtt_p95_ms, rtt_max_ms,
		       jitter_ms, gaps60, gaps150, gap_max_ms, reorder, dup, error
		FROM voice_runs
		WHERE measured_at >= $1 AND ($2 = '' OR install_id = $2) AND ($3 = '' OR proto = $3)
		ORDER BY measured_at ASC
		LIMIT 20000
	`, q.Since, q.Install, q.Proto)
	if err != nil {
		return nil, fmt.Errorf("read voice runs: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var jitter *float32
		if err := rows.Scan(&r.ID, &r.InstallID, &r.MeasuredAt, &r.ClientVersion, &r.OnRequest, &r.Path,
			&r.Profile, &r.Proto, &r.Iface, &r.Network, &r.NetworkAt, &r.Mobile, &r.Sent, &r.Recv, &r.LossRun,
			&r.RTTP50, &r.RTTP95, &r.RTTMax, &jitter, &r.Gaps60, &r.Gaps150, &r.GapMax,
			&r.Reorder, &r.Dup, &r.Error); err != nil {
			return nil, fmt.Errorf("scan voice run: %w", err)
		}
		if jitter != nil {
			j := math.Round(float64(*jitter)*10) / 10
			r.Jitter = &j
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Verdicts of a group, by the owner's stop rule.
const (
	VerdictOK      = "ok"      // silences over 60 ms rarer than one a minute
	VerdictAudible = "audible" // one a minute or more: a call hears it
	VerdictNoData  = "no_data" // no series measured anything
)

// audiblePerMinute is the stop rule's line.
const audiblePerMinute = 1.0

// seriesInterval is the client's send interval: sent × it is the series' time.
const seriesInterval = 20 * time.Millisecond

// Group is one tester's series on one path, profile kind and network.
type Group struct {
	InstallID string  `json:"install_id"`
	Alias     string  `json:"alias,omitempty"`
	Path      string  `json:"path"`
	Proto     string  `json:"proto,omitempty"`
	Network   string  `json:"network"`
	Mobile    bool    `json:"mobile"`
	Series    int     `json:"series"`
	Failed    int     `json:"failed"` // series that ended in an error: not in the numbers
	Minutes   float64 `json:"minutes"`
	LossPct   float64 `json:"loss_pct"`
	// Gaps60PerMin and Gaps150PerMin are the silences a call hears, a minute.
	Gaps60PerMin  float64   `json:"gaps60_per_min"`
	Gaps150PerMin float64   `json:"gaps150_per_min"`
	GapMaxMs      *int      `json:"gap_max_ms,omitempty"`
	RTTP50Ms      *int      `json:"rtt_p50_ms,omitempty"` // median of the series' medians
	JitterMs      *float64  `json:"jitter_ms,omitempty"`  // median of the series' jitter
	Verdict       string    `json:"verdict"`
	Last          time.Time `json:"last"`
}

// Summarize groups rows by tester, path, protocol and network, newest first.
func Summarize(rows []Row) []Group {
	type acc struct {
		g               Group
		sent, recv      int
		gaps60, gaps150 int
		p50s            []int
		jitters         []float64
	}
	groups := map[string]*acc{}
	var order []string
	for _, r := range rows {
		proto := r.Proto
		if r.Path == "direct" {
			proto = "" // the bare network: no profile
		}
		key := strings.Join([]string{r.InstallID, r.Path, proto, r.Network, strconv.FormatBool(r.Mobile)}, "|")
		a := groups[key]
		if a == nil {
			a = &acc{g: Group{InstallID: r.InstallID, Alias: r.Alias, Path: r.Path, Proto: proto, Network: r.Network, Mobile: r.Mobile}}
			groups[key] = a
			order = append(order, key)
		}
		a.g.Series++
		if r.MeasuredAt.After(a.g.Last) {
			a.g.Last = r.MeasuredAt
		}
		// A series that ended in an error (the session restarted, the network
		// went down) measured part of what a call hears, or nothing: counted as
		// a failure, it does not stand in the numbers for a whole one.
		if r.Sent == 0 || r.Error != "" {
			a.g.Failed++
			continue
		}
		a.sent += r.Sent
		a.recv += r.Recv
		a.gaps60 += r.Gaps60
		a.gaps150 += r.Gaps150
		if r.GapMax != nil && (a.g.GapMaxMs == nil || *r.GapMax > *a.g.GapMaxMs) {
			v := *r.GapMax
			a.g.GapMaxMs = &v
		}
		if r.RTTP50 != nil {
			a.p50s = append(a.p50s, *r.RTTP50)
		}
		if r.Jitter != nil {
			a.jitters = append(a.jitters, *r.Jitter)
		}
	}
	out := make([]Group, 0, len(order))
	for _, key := range order {
		a := groups[key]
		g := a.g
		if a.sent == 0 {
			g.Verdict = VerdictNoData
			out = append(out, g)
			continue
		}
		minutes := (time.Duration(a.sent) * seriesInterval).Minutes()
		g.Minutes = round1(minutes)
		g.LossPct = round2(float64(a.sent-a.recv) / float64(a.sent) * 100)
		g.Gaps60PerMin = round2(float64(a.gaps60) / minutes)
		g.Gaps150PerMin = round2(float64(a.gaps150) / minutes)
		if len(a.p50s) > 0 {
			sort.Ints(a.p50s)
			v := a.p50s[(len(a.p50s)-1)/2]
			g.RTTP50Ms = &v
		}
		if len(a.jitters) > 0 {
			sort.Float64s(a.jitters)
			v := a.jitters[(len(a.jitters)-1)/2]
			g.JitterMs = &v
		}
		g.Verdict = VerdictOK
		if g.Gaps60PerMin >= audiblePerMinute {
			g.Verdict = VerdictAudible
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
