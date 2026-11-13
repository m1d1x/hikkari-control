package stand

// The cascade's synthetic users (hikkari-client
// plans/research/2026-11-11-entry-cpu-jitter-after-stage4.md): a task of its
// own kind, LoadTaskKind, that a point in Russia runs on demand to put the
// load two testers put on the trunk entry, and to hear what it does to them.
// Each user is a core of its own on the profile through the entry (a Hikkari
// session of its own, as a person's client is), and through it two live UDP
// streams to a UDP echo beyond the exit (an edge provoke receiver,
// edge/provoke.go): a call, 50 packets a second of an Opus frame's size, and a
// game, GamePPS packets a second of a game update's size. Every packet comes
// back, so the load runs both ways, and each stream is read as a call is:
// pauses between answers over 60 and 150 ms, the longest, round trips,
// jitter, and the pauses over 60 ms of each minute. This is a simulation of
// users, named so in every row it writes; the trunk's own call (layer call)
// says in the same minutes what the entry did to everyone.
//
// The same streams run on two control paths past the entry, so a run through
// it reads against what the legs around it give on their own (Path): direct,
// the core on the Hikkari profile straight to the exit's relay (the client's
// core, the border, the exit and the exit to the echo); raw, the streams from
// the point straight to the echo with no core and no tunnel (the point and
// its network to the echo).

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LoadTaskKind is the knob column of a synthetic users task.
const LoadTaskKind = "cascade-load-v1"

// LoadSpec is what a synthetic users task runs.
type LoadSpec struct {
	// Users is how many people, each a core and a session of its own.
	Users int
	// Minutes is how long every stream runs.
	Minutes int
	// GamePPS is each user's game stream in packets a second, 0 for none.
	GamePPS int
	// Echo is the UDP echo's address beyond the exit, ip:port.
	Echo netip.AddrPort
	// Path is the way the streams go: LoadPathEntry, LoadPathDirect or
	// LoadPathRaw; "" reads as LoadPathEntry (a task queued before paths).
	Path string
}

// The paths a synthetic users task runs on.
const (
	// LoadPathEntry: a core on the profile through the entry.
	LoadPathEntry = "entry"
	// LoadPathDirect: a core on the Hikkari profile straight to the exit.
	LoadPathDirect = "direct"
	// LoadPathRaw: plain UDP from the point to the echo, no core.
	LoadPathRaw = "raw"
	// LoadPathPipe: a core on the profile through the stand over hk-pipe
	// beside the entry (roadmap 4.6, R5), the same hour as LoadPathEntry.
	LoadPathPipe = "pipe"
)

// path is s.Path with the default filled in.
func (s LoadSpec) path() string {
	if s.Path == "" {
		return LoadPathEntry
	}
	return s.Path
}

const (
	loadMaxUsers    = 8
	loadMaxMinutes  = 30
	loadMaxGamePPS  = 500
	loadVoicePPS    = 50  // one Opus frame every 20 ms
	loadVoiceBytes  = 160 // an Opus frame
	loadGameBytes   = 80  // a game's state update
	loadEchoMagic   = "HKP1"
	loadWarmup      = 8 * time.Second
	loadTail        = 2 * time.Second
	loadDoorStagger = 500 * time.Millisecond
)

// Values are the spec as a task's vals.
func (s LoadSpec) Values() []string {
	return []string{
		"users=" + strconv.Itoa(s.Users),
		"minutes=" + strconv.Itoa(s.Minutes),
		"game_pps=" + strconv.Itoa(s.GamePPS),
		"echo=" + s.Echo.String(),
		"path=" + s.path(),
	}
}

// Validate says what is wrong with s, nil when nothing is.
func (s LoadSpec) Validate() error {
	switch {
	case s.Users < 1 || s.Users > loadMaxUsers:
		return fmt.Errorf("users: от 1 до %d", loadMaxUsers)
	case s.Minutes < 1 || s.Minutes > loadMaxMinutes:
		return fmt.Errorf("minutes: от 1 до %d", loadMaxMinutes)
	case s.GamePPS < 0 || s.GamePPS > loadMaxGamePPS:
		return fmt.Errorf("game_pps: от 0 до %d", loadMaxGamePPS)
	case !s.Echo.IsValid() || s.Echo.Port() == 0:
		return errors.New("echo: нужен адрес ip:port")
	case !s.Echo.Addr().Is4() || !s.Echo.Addr().IsGlobalUnicast() || s.Echo.Addr().IsPrivate():
		return errors.New("echo: нужен публичный IPv4")
	}
	switch s.path() {
	case LoadPathEntry, LoadPathDirect, LoadPathRaw, LoadPathPipe:
	default:
		return fmt.Errorf("path: %s, %s, %s или %s", LoadPathEntry, LoadPathPipe, LoadPathDirect, LoadPathRaw)
	}
	// The run must end well inside the claim it holds (claimTTL).
	if time.Duration(s.Minutes)*time.Minute+time.Duration(s.Users)*loadDoorStagger+5*time.Minute > claimTTL {
		return errors.New("задача не уложится в срок захвата")
	}
	return nil
}

// ParseLoadSpec reads a task's vals.
func ParseLoadSpec(vals []string) (LoadSpec, error) {
	var s LoadSpec
	seen := map[string]bool{}
	for _, v := range vals {
		k, val, ok := strings.Cut(v, "=")
		if !ok || seen[k] {
			return s, fmt.Errorf("значение задачи %q", v)
		}
		seen[k] = true
		var err error
		switch k {
		case "users":
			s.Users, err = strconv.Atoi(val)
		case "minutes":
			s.Minutes, err = strconv.Atoi(val)
		case "game_pps":
			s.GamePPS, err = strconv.Atoi(val)
		case "echo":
			s.Echo, err = netip.ParseAddrPort(val)
		case "path":
			s.Path = val
		default:
			return s, fmt.Errorf("значение задачи %q", v)
		}
		if err != nil {
			return s, fmt.Errorf("значение задачи %q: %v", v, err)
		}
	}
	for _, k := range []string{"users", "minutes", "game_pps", "echo"} {
		if !seen[k] {
			return s, fmt.Errorf("в задаче нет %s", k)
		}
	}
	if s.Path == "" {
		s.Path = LoadPathEntry
	}
	return s, s.Validate()
}

// LoadStreamLabel names a stream of a user in a task's result.
func LoadStreamLabel(user int, stream string) string {
	return "user" + strconv.Itoa(user) + "/" + stream
}

// runLoad runs the spec's users on its path: a core each on the task's
// profile through the entry or straight to the exit, or plain UDP to the echo.
func (r Runner) runLoad(ctx context.Context, t Task, profiles []Profile) Result {
	res := Result{Core: r.Kit.Label}
	spec, err := ParseLoadSpec(t.Values)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var through *Profile
	if spec.Path != LoadPathRaw {
		prefix := cascadeEntryLabelPrefix
		switch spec.Path {
		case LoadPathDirect:
			prefix = cascadeDirectLabelPrefix
		case LoadPathPipe:
			prefix = cascadePipeLabelPrefix
		}
		for i := range profiles {
			if strings.HasPrefix(profiles[i].Label, prefix) {
				through = &profiles[i]
				break
			}
		}
		if through == nil {
			res.Error = "в задаче нет профиля " + prefix + "*"
			return res
		}
	} else {
		res.Core = "none"
	}
	echo := spec.Echo.String()
	var (
		mu      sync.Mutex
		streams []CascadeVoice
		errs    []string
		wg      sync.WaitGroup
	)
	// Every stream ends at the same moment, so the minutes line up.
	start := time.Now().Add(loadWarmup + time.Duration(spec.Users)*loadDoorStagger)
	end := start.Add(time.Duration(spec.Minutes) * time.Minute)
	for u := 1; u <= spec.Users; u++ {
		// raw: the streams go to the echo itself, each user from sockets of
		// its own as a person's apps would.
		to := net.UDPAddrFromAddrPort(spec.Echo)
		if through != nil {
			door, stop, err := startVoiceDoor(ctx, r.Kit.Core, through.Config, echo)
			if err != nil {
				errs = append(errs, fmt.Sprintf("user%d: %v", u, err))
				continue
			}
			defer stop()
			to = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: door}
		}
		run := func(name string, pps, size int) {
			defer wg.Done()
			v := liveStream(ctx, to, LoadStreamLabel(u, name), pps, size, start, end)
			mu.Lock()
			streams = append(streams, v)
			mu.Unlock()
		}
		wg.Add(1)
		go run("voice", loadVoicePPS, loadVoiceBytes)
		if spec.GamePPS > 0 {
			wg.Add(1)
			go run("game", spec.GamePPS, loadGameBytes)
		}
		select {
		case <-ctx.Done():
		case <-time.After(loadDoorStagger):
		}
	}
	wg.Wait()
	sort.Slice(streams, func(i, j int) bool { return streams[i].Profile < streams[j].Profile })
	res.Voice = streams
	for _, v := range streams {
		r.log("stand: load %s: %d/%d gaps60=%d gaps150=%d gap_max=%.0f", v.Profile, v.Got, v.Sent, v.Gaps60, v.Gaps150, v.GapMax)
	}
	if len(streams) == 0 && len(errs) == 0 {
		errs = append(errs, "ни один поток не запустился")
	}
	res.Error = joinErrs(errs)
	return res
}

// How the profiles through the entry and straight to the exit are labelled
// (subscription.CascadeProbeLabel(subscription.CascadeProbeEntry and
// CascadeProbeDirect, relay)).
const (
	cascadeEntryLabelPrefix  = "cascade-entry-"
	cascadePipeLabelPrefix   = "cascade-pipe-"
	cascadeDirectLabelPrefix = "cascade-direct-"
)

// liveStream sends pps packets of size bytes a second to `to` (a core's door
// or the echo itself) from start to end, after an answered warm-up, and reads
// them back as a call.
func liveStream(ctx context.Context, to *net.UDPAddr, label string, pps, size int, start, end time.Time) CascadeVoice {
	v := CascadeVoice{Profile: label}
	conn, err := net.DialUDP("udp4", nil, to)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	defer conn.Close()
	var id [8]byte
	_, _ = rand.Read(id[:])
	buf := make([]byte, 2048)
	warmed := false
	for deadline := time.Now().Add(loadWarmup); !warmed && time.Now().Before(deadline) && ctx.Err() == nil; {
		_, _ = conn.Write(echoPacket(id, 1<<31, size))
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if n, err := conn.Read(buf); err == nil {
			_, warmed = echoAnswer(buf[:n], id)
		}
	}
	interval := time.Second / time.Duration(pps)
	packets := int(end.Sub(start) / interval)
	v.Sent = packets
	if !warmed {
		v.Error = "эхо не ответило на прогрев"
		v.LossRun = packets
		return v
	}
	_ = conn.SetReadDeadline(time.Time{})
	select {
	case <-ctx.Done():
		v.Error = "cancelled"
		return v
	case <-time.After(time.Until(start)):
	}
	var amu sync.Mutex
	sentAt := make([]time.Time, packets)
	var answers []echoArrival
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 2048)
		for {
			n, err := conn.Read(b)
			if err != nil {
				return
			}
			seq, ok := echoAnswer(b[:n], id)
			if !ok || int(seq) >= packets {
				continue
			}
			amu.Lock()
			answers = append(answers, echoArrival{seq, time.Now()})
			amu.Unlock()
		}
	}()
	tick := time.NewTicker(interval)
	for seq := 0; seq < packets; seq++ {
		amu.Lock()
		sentAt[seq] = time.Now()
		amu.Unlock()
		_, _ = conn.Write(echoPacket(id, uint32(seq), size))
		select {
		case <-ctx.Done():
			tick.Stop()
			v.Error = "cancelled"
			return v
		case <-tick.C:
		}
	}
	tick.Stop()
	_ = conn.SetReadDeadline(time.Now().Add(loadTail))
	<-done
	amu.Lock()
	defer amu.Unlock()
	callStats(&v, sentAt, func(yield func(uint32, time.Time) bool) {
		for _, a := range answers {
			if !yield(a.seq, a.at) {
				return
			}
		}
	})
	v.Gaps60PerMinute = gapsPerMinute(answers, start, int(end.Sub(start)/time.Minute))
	return v
}

type echoArrival struct {
	seq uint32
	at  time.Time
}

// gapsPerMinute counts the pauses over 60 ms between answers in each minute
// from start, by when the pause ended; the first answer of each seq only, as
// callStats counts them.
func gapsPerMinute(answers []echoArrival, start time.Time, minutes int) []int {
	if minutes <= 0 {
		return nil
	}
	out := make([]int, minutes)
	seen := map[uint32]bool{}
	var last time.Time
	for _, a := range answers {
		if seen[a.seq] {
			continue
		}
		seen[a.seq] = true
		if !last.IsZero() && a.at.Sub(last) > 60*time.Millisecond {
			if m := int(a.at.Sub(start) / time.Minute); m >= 0 && m < minutes {
				out[m]++
			}
		}
		last = a.at
	}
	return out
}

// echoPacket is a packet the provoke receiver echoes: its magic, the
// stream's id, seq, padded to size.
func echoPacket(id [8]byte, seq uint32, size int) []byte {
	if size < len(loadEchoMagic)+12 {
		size = len(loadEchoMagic) + 12
	}
	b := make([]byte, size)
	copy(b, loadEchoMagic)
	copy(b[len(loadEchoMagic):], id[:])
	binary.BigEndian.PutUint32(b[len(loadEchoMagic)+8:], seq)
	return b
}

func echoAnswer(b []byte, id [8]byte) (uint32, bool) {
	m := len(loadEchoMagic)
	if len(b) < m+12 || string(b[:m]) != loadEchoMagic || string(b[m:m+8]) != string(id[:]) {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[m+8:]), true
}

// QueueLoad queues a synthetic users task of entryBox on slot, unless the
// slot already has one not done: two at once would be twice the users.
func (s *Scheduler) QueueLoad(ctx context.Context, slot, entryBox string, spec LoadSpec) (uuid.UUID, error) {
	if err := spec.Validate(); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO stand_tasks (slot, knob, vals, base, rounds, baseline_path, cascade_entry, cascade_round)
		SELECT $1, $2, $3, '', 1, '/', $4, now()
		WHERE NOT EXISTS (SELECT 1 FROM stand_tasks WHERE slot = $1 AND knob = $2 AND status IN ('queued', 'claimed'))
		RETURNING id`, slot, LoadTaskKind, spec.Values(), entryBox).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrLoadBusy
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("queue load task: %w", err)
	}
	return id, nil
}

// ErrLoadBusy: the slot already has a synthetic users task not done.
var ErrLoadBusy = errors.New("на точке уже идёт нагрузка")

// finishLoad records a synthetic users task: its streams go to the watch,
// and the task is done, or failed when the point says why it stopped short.
func (s *Scheduler) finishLoad(ctx context.Context, slot string, id uuid.UUID, res Result) error {
	if len(res.Rows) > 0 {
		return fmt.Errorf("%w: rows in a load task", ErrNotAResult)
	}
	if len(res.Voice) > 2*loadMaxUsers {
		return fmt.Errorf("%w: %d streams", ErrNotAResult, len(res.Voice))
	}
	for _, v := range res.Voice {
		if len(v.Gaps60PerMinute) > loadMaxMinutes {
			return fmt.Errorf("%w: %d minutes in a stream", ErrNotAResult, len(v.Gaps60PerMinute))
		}
	}
	var vals []string
	var entryBox string
	var queued time.Time
	if err := s.pool.QueryRow(ctx, `SELECT vals, coalesce(cascade_entry, ''), coalesce(cascade_round, created_at) FROM stand_tasks WHERE id = $1`, id).
		Scan(&vals, &entryBox, &queued); err != nil {
		return fmt.Errorf("load task: %w", err)
	}
	spec, err := ParseLoadSpec(vals)
	if err != nil {
		return fmt.Errorf("load task: %w", err)
	}
	if s.cascade == nil {
		return errors.New("stand: load task with no cascade watch")
	}
	if err := s.cascade.LoadFinished(ctx, slot, entryBox, spec, queued, res); err != nil {
		return fmt.Errorf("cascade watch: %w", err)
	}
	status, reason := TaskDone, strings.TrimSpace(res.Error)
	if reason != "" {
		status = TaskFailed
		if len(reason) > 2000 {
			reason = reason[:2000]
		}
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE stand_tasks SET status = $2, error = $3, core = $4, rows_sent = $5, rows_added = 0, finished_at = now(), links_token_hash = NULL
		WHERE id = $1`, id, status, reason, res.Core, len(res.Voice)); err != nil {
		return fmt.Errorf("finish load task: %w", err)
	}
	return nil
}
