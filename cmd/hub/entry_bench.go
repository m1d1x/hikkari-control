package main

// entry-bench: bench a relay box from the hub's shell (roadmap 42 П6.14 Ф3).
// Queues the run a rollout makes before and after a swap, waits for a vantage
// to drive it, and prints the summary. For trying a box and the vantage by
// hand; the rollout queues its own.
//
//	cmd/server entry-bench <box> [seconds]   # default 300, 60..600

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

func runEntryBench(args []string) {
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "entry-bench: %v\n", err)
		os.Exit(1)
	}
	if len(args) != 1 && len(args) != 2 {
		fail(fmt.Errorf("usage: cmd/server entry-bench <box> [seconds]"))
	}
	d := entry.DefaultBenchDuration
	if len(args) == 2 {
		sec, err := strconv.Atoi(args[1])
		if err != nil {
			fail(fmt.Errorf("seconds must be a number"))
		}
		d = time.Duration(sec) * time.Second
	}
	if err := cryptobox.Configure(os.Getenv); err != nil {
		fail(fmt.Errorf("content key: %w", err))
	}
	_, cancel, pool := adminCLIPool()
	cancel() // the wait outlives the CLI's 30 s connect budget
	defer pool.Close()
	svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	id, err := svc.QueueBench(ctx, uuid.Nil, args[0], "", entry.BenchManual, d)
	if err != nil {
		fail(err)
	}
	fmt.Printf("бенч %s поставлен (%s), ждём точку обзора\n", id, d)
	res, err := svc.WaitBench(ctx, id, d, 2*time.Minute, 2*time.Second)
	if err != nil {
		fail(err)
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if res.Error != "" {
		os.Exit(1)
	}
	// A subcommand ends here: returning would fall through into the server's
	// own start-up in the same process (it did, on the stand: a second hub
	// came up beside the live one until its gRPC port was taken).
	os.Exit(0)
}

// runEntryVantageToken issues a new bearer for an active observation point
// slot and prints it (audit 42-autopilot К1). For a slot whose machine the hub
// does not reach — a standalone VPS such as the stand's nekko-spb — the owner
// carries it there by hand (scripts/edge/deploy-prober.sh). The slot's old
// token stops working at once; the hub keeps only the new one's hash.
//
//	cmd/server entry-vantage-token <slot>
func runEntryVantageToken(args []string) {
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "entry-vantage-token: %v\n", err)
		os.Exit(1)
	}
	if len(args) != 1 {
		fail(fmt.Errorf("usage: cmd/server entry-vantage-token <slot>"))
	}
	if err := cryptobox.Configure(os.Getenv); err != nil {
		fail(fmt.Errorf("content key: %w", err))
	}
	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()
	svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
	if err != nil {
		fail(err)
	}
	token, err := svc.IssueVantageToken(ctx, args[0])
	if err != nil {
		fail(err)
	}
	fmt.Println(token)
	os.Exit(0)
}

// benchSink takes the hub's finished bench runs into ClickHouse.
type benchSink struct{ store *analytics.Store }

func (b benchSink) InsertEntryBench(ctx context.Context, r entry.BenchRecord) error {
	x := r.Result
	return b.store.InsertEntryBench(ctx, analytics.EntryBenchRecord{
		Timestamp: r.At, TaskID: r.TaskID.String(), SourceRegion: r.Region, RelayName: r.Relay,
		Box: r.Box, Arch: r.Arch, EdgeSHA: r.EdgeSHA, Phase: r.Phase,
		DurationS: float32(x.DurationS), ChatN: uint32(x.ChatN), ChatErrors: uint32(x.ChatErrors),
		ChatRTTP50Ms: float32(x.ChatRTTP50Ms), ChatRTTP95Ms: float32(x.ChatRTTP95Ms),
		MediaN: uint32(x.MediaN), MediaErrors: uint32(x.MediaErrors),
		DownMbps: float32(x.DownMbps), UpMbps: float32(x.UpMbps),
		BytesDown: uint64(x.BytesDown), BytesUp: uint64(x.BytesUp),
		VoiceSent: uint32(x.VoiceSent), VoiceLost: uint32(x.VoiceLost), VoiceLate: uint32(x.VoiceLate),
		VoiceRTTP50: float32(x.VoiceRTTP50Ms), VoiceJitter: float32(x.VoiceJitterMs),
		VoiceDrops: uint32(x.VoiceDrops), Drops: uint32(x.Drops), Error: x.Error,
	})
}

// reachSink joins the entry domain to the dataset for client reach telemetry
// (plan 42 П9.9), as benchSink does for bench runs. Unset when analytics is
// off: the hub then validates reach reports and drops them, and only the
// console's doorway metric goes missing.
type reachSink struct{ store *analytics.Store }

func (s reachSink) InsertEntryReach(ctx context.Context, rows []entry.ReachRecord) error {
	out := make([]analytics.EntryReachRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, analytics.EntryReachRecord{
			Timestamp:   r.TS,
			Account:     r.Account,
			Outcome:     r.Outcome,
			RelayName:   r.Relay,
			RelaysTried: uint8(r.RelaysTried),
			ConnectMs:   uint32(r.ConnectMS),
			RTTMs:       uint32(r.RTTMS),
		})
	}
	return s.store.InsertEntryReach(ctx, out)
}

// pulseSink joins the entry domain to the dataset for relay self-reports
// (plan 42 П9.3). Unset when analytics is off: a pulse is then validated,
// accepted and dropped, which keeps the boxes' loop healthy while only the
// console's load figures go missing.
type pulseSink struct{ store *analytics.Store }

func (s pulseSink) InsertEntryRelayStats(ctx context.Context, rows []entry.PulseRecord) error {
	out := make([]analytics.EntryRelayStatRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, analytics.EntryRelayStatRecord{
			Timestamp: r.TS,
			RelayName: r.Relay,
			Proto:     r.Proto,
			UptimeS:   uint32(r.UptimeS),
			BytesUp:   uint64(r.BytesUp),
			BytesDown: uint64(r.BytesDown),
			Conns:     uint64(r.Conns),
			CPUPct:    float32(r.CPUPct),
			MemMB:     uint32(r.MemMB),
			Version:   r.Version,
		})
	}
	return s.store.InsertEntryRelayStats(ctx, out)
}

// loadSource joins the fleet's sizing to the dataset the relays' pulses and
// the clients' reach reports land in (plan 42 П9.14–П9.15). Unset when
// analytics is off: the fleet then shows neither demand nor capacity.
type loadSource struct{ store *analytics.Store }

func (s loadSource) Load(ctx context.Context, since time.Time, bucket time.Duration, groups map[string]string) ([]entry.LoadPoint, error) {
	rows, err := s.store.EntryLoad(ctx, since, bucket, groups)
	if err != nil {
		return nil, err
	}
	out := make([]entry.LoadPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, entry.LoadPoint{Key: r.Key, At: r.Bucket, Mbps: r.Mbps, VolumeMbps: r.VolumeMbps, CPUPct: r.CPUPct})
	}
	return out, nil
}

func (s loadSource) ReachRTT(ctx context.Context, since time.Time, bucket time.Duration, groups map[string]string) ([]entry.RTTPoint, error) {
	rows, err := s.store.EntryReachRTT(ctx, since, bucket, groups)
	if err != nil {
		return nil, err
	}
	out := make([]entry.RTTPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, entry.RTTPoint{Key: r.Key, At: r.Bucket, P95Ms: r.P95Ms, N: r.N})
	}
	return out, nil
}

func (s loadSource) BenchPeaks(ctx context.Context, since time.Time) (map[string]float64, error) {
	return s.store.EntryBenchPeaks(ctx, since)
}

// userTrafficSink joins the entry domain to the dataset for the gateway's
// per-socket byte counts (plan 42 П9.9). Unset when analytics is off: the
// counts are then dropped and only the console's per-person figures go
// missing.
type userTrafficSink struct{ store *analytics.Store }

func (s userTrafficSink) InsertEntryUserTraffic(ctx context.Context, rows []entry.UserTrafficRecord) error {
	out := make([]analytics.EntryUserTrafficRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, analytics.EntryUserTrafficRecord{
			Timestamp: r.TS,
			Account:   r.Account,
			Box:       r.Box,
			BytesUp:   uint64(r.BytesUp),
			BytesDown: uint64(r.BytesDown),
		})
	}
	return s.store.InsertEntryUserTraffic(ctx, out)
}

// verdictSource lets the incident detector read what the RU vantage last saw
// (plan 42 П9.5), and tells the hub how many vantages there are at all (К3).
// The probe dataset is ClickHouse's; the detector's rules are the domain's and
// are tested without either.
type verdictSource struct{ store *analytics.Store }

// Vantages lists the observation points that have reported in [since, until].
//
// The hub asks this for one decision: whether two-vantage confirmation of a
// ban can be required. With one RU vantage the answer is no, and the hub says
// so rather than storing a rule that would quietly stop every eviction. Only
// the active slots among these count (entry.Service.Vantages).
func (v verdictSource) Vantages(ctx context.Context, since, until time.Time) ([]string, error) {
	const q = `
		SELECT DISTINCT source_region
		FROM entry_probes
		WHERE ts >= ? AND ts <= ? AND source_region != ''
		ORDER BY source_region
	`
	rows, err := v.store.Query(ctx, q, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var region string
		if err := rows.Scan(&region); err != nil {
			return nil, err
		}
		out = append(out, region)
	}
	return out, rows.Err()
}

// LatestVerdictsByVantage returns each observation point's latest verdict per
// relay in [since, until], so the detector can apply two-vantage confirmation
// and ignore a single vantage's network outage (roadmap 42 К3 / К3′).
//
// argMax takes each relay's verdict from its newest row, so the upper bound
// is not decoration: a row dated in the future — written before the ingest
// refused them, or by a clock gone wrong — would otherwise be "the newest"
// until the table's TTL, whatever the vantage said since (audit
// 42-autopilot К1).
func (v verdictSource) LatestVerdictsByVantage(ctx context.Context, since, until time.Time) (map[string]entry.RelayVerdicts, error) {
	const q = `
		SELECT source_region, relay_name, argMax(verdict, ts)
		FROM entry_probes
		WHERE ts >= ? AND ts <= ? AND source_region != ''
		GROUP BY source_region, relay_name
	`
	rows, err := v.store.Query(ctx, q, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]entry.RelayVerdicts{}
	for rows.Next() {
		var region, relay, verdict string
		if err := rows.Scan(&region, &relay, &verdict); err != nil {
			return nil, err
		}
		if out[region] == nil {
			out[region] = entry.RelayVerdicts{}
		}
		out[region][relay] = verdict
	}
	return out, rows.Err()
}
