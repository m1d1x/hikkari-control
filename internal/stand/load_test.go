package stand

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

// A task's vals carry the spec there and back, and a spec out of bounds or
// with an echo that is not a public IPv4 is refused.
func TestLoadSpecValues(t *testing.T) {
	spec := LoadSpec{Users: 2, Minutes: 10, GamePPS: 200, Echo: netip.MustParseAddrPort("203.0.113.7:2053")}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := ParseLoadSpec(spec.Values())
	if err != nil || got != spec {
		t.Fatalf("round trip %+v %v", got, err)
	}
	bad := []LoadSpec{
		{Users: 0, Minutes: 10, Echo: spec.Echo},
		{Users: 9, Minutes: 10, Echo: spec.Echo},
		{Users: 2, Minutes: 0, Echo: spec.Echo},
		{Users: 2, Minutes: 31, Echo: spec.Echo},
		{Users: 2, Minutes: 10, GamePPS: 501, Echo: spec.Echo},
		{Users: 2, Minutes: 10, Echo: netip.MustParseAddrPort("10.0.0.1:2053")},
		{Users: 2, Minutes: 10, Echo: netip.MustParseAddrPort("127.0.0.1:2053")},
		{Users: 2, Minutes: 10, Echo: netip.MustParseAddrPort("[2001:db8::1]:2053")},
		{Users: 2, Minutes: 10, Echo: netip.MustParseAddrPort("203.0.113.7:0")},
	}
	for _, b := range bad {
		if b.Validate() == nil {
			t.Errorf("%+v passed", b)
		}
	}
	for _, vals := range [][]string{
		{"users=2", "minutes=10", "game_pps=0"},
		{"users=2", "users=3", "minutes=10", "game_pps=0", "echo=203.0.113.7:2053"},
		{"users=2", "minutes=10", "game_pps=0", "echo=203.0.113.7:2053", "x=1"},
	} {
		if _, err := ParseLoadSpec(vals); err == nil {
			t.Errorf("%q parsed", vals)
		}
	}
}

// The echo's packet: its magic, the stream's id and seq; another stream's
// answer or a stranger's packet is not ours.
func TestEchoPacket(t *testing.T) {
	id := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	p := echoPacket(id, 42, 80)
	if len(p) != 80 || string(p[:4]) != loadEchoMagic {
		t.Fatalf("packet %x", p)
	}
	if seq, ok := echoAnswer(p, id); !ok || seq != 42 {
		t.Fatalf("answer %d %v", seq, ok)
	}
	if _, ok := echoAnswer(p, [8]byte{9}); ok {
		t.Fatal("another stream's answer read as ours")
	}
	if _, ok := echoAnswer([]byte("HKP1short"), id); ok {
		t.Fatal("a short packet read as ours")
	}
}

// Pauses over 60 ms land in the minute they ended in; a copy is counted once.
func TestGapsPerMinute(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	answers := []echoArrival{
		{0, at(0)}, {1, at(20 * time.Millisecond)},
		{2, at(200 * time.Millisecond)}, // pause in minute 0
		{2, at(210 * time.Millisecond)}, // a copy
		{3, at(61 * time.Second)},       // pause ending in minute 1
		{4, at(61*time.Second + 20*time.Millisecond)},
		{5, at(130 * time.Second)}, // beyond the run's minutes
	}
	got := gapsPerMinute(answers, t0, 2)
	if len(got) != 2 || got[0] != 1 || got[1] != 1 {
		t.Fatalf("per minute %v", got)
	}
}

// A stream through a live UDP echo (here in place of the core's door): every
// packet answered, and a pause the echo takes is heard as a call hears it.
func TestLiveStreamHearsTheEchosPause(t *testing.T) {
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	var pauseAt atomic.Int64 // unix nanos: hold answers 200 ms once, from then
	go func() {
		buf := make([]byte, 2048)
		held := false
		for {
			n, from, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if p := pauseAt.Load(); !held && p != 0 && time.Now().UnixNano() >= p {
				held = true
				time.Sleep(200 * time.Millisecond)
			}
			_, _ = echo.WriteToUDP(buf[:n], from)
		}
	}()
	door := echo.LocalAddr().(*net.UDPAddr).Port
	start := time.Now().Add(200 * time.Millisecond)
	end := start.Add(2 * time.Second)
	pauseAt.Store(start.Add(time.Second).UnixNano())
	v := liveStream(context.Background(), door, "user1/voice", 50, loadVoiceBytes, start, end)
	if v.Error != "" {
		t.Fatal(v.Error)
	}
	if v.Sent != 100 || v.Got != 100 {
		t.Fatalf("sent %d got %d", v.Sent, v.Got)
	}
	if v.Gaps150 != 1 || v.GapMax < 150 {
		t.Fatalf("gaps150 %d gap_max %.0f, want the 200 ms pause heard once", v.Gaps150, v.GapMax)
	}
	if len(v.Gaps60PerMinute) != 0 {
		t.Fatalf("per minute %v for a run under a minute", v.Gaps60PerMinute)
	}
}

// An echo that never answers is said so, not read as a clean stream.
func TestLiveStreamWithNoEcho(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the warm-up")
	}
	dead, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()
	start := time.Now()
	v := liveStream(context.Background(), dead.LocalAddr().(*net.UDPAddr).Port, "user1/game", 100, loadGameBytes, start, start.Add(time.Second))
	if v.Error == "" || v.Got != 0 || v.LossRun != v.Sent {
		t.Fatalf("%+v", v)
	}
}

// A synthetic users task: queued once per point, handed out with the stand
// switched off, served the watch's links, its streams go to the watch and
// nowhere else, and the point may queue the next once it is done.
func TestDBLoadTaskReachesTheWatch(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, false)
	s := newTestScheduler(pool, fakeSlots{"ru-eurodir"}, relayruns.NewStore(pool))
	watch := &fakeCascade{links: []string{"hikkari+v1://hikkari:k@192.0.2.1:8443?sni=x.example#cascade-entry-unl1-2"}}
	s.SetCascade(watch)
	spec := LoadSpec{Users: 2, Minutes: 10, GamePPS: 200, Echo: netip.MustParseAddrPort("203.0.113.7:2053")}
	id, err := s.QueueLoad(ctx, "ru-eurodir", "ru-entry", spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueLoad(ctx, "ru-eurodir", "ru-entry", spec); !errors.Is(err, ErrLoadBusy) {
		t.Fatalf("a second task while the first is not done: %v", err)
	}
	task, err := s.Claim(ctx, "ru-eurodir")
	if err != nil || task == nil || task.ID != id || task.Knob != LoadTaskKind {
		t.Fatalf("claimed %+v %v", task, err)
	}
	if got, err := ParseLoadSpec(task.Values); err != nil || got != spec {
		t.Fatalf("handed out %+v %v", got, err)
	}
	links, err := s.FleetLinks(ctx, linksTokenOf(t, task))
	if err != nil || len(links) != 1 || links[0] != watch.links[0] {
		t.Fatalf("links %q %v", links, err)
	}
	if _, err := s.Finish(ctx, "ru-eurodir", task.ID, Result{Rows: []Row{{Profile: "x"}}}); !errors.Is(err, ErrNotAResult) {
		t.Fatalf("rows in a load task: %v", err)
	}
	res := Result{Core: "sing-box-lx test", Voice: []CascadeVoice{
		{Profile: "user1/voice", Sent: 30000, Got: 29990, Gaps60: 40, Gaps60PerMinute: []int{4, 4, 4, 4, 4, 4, 4, 4, 4, 4}},
		{Profile: "user1/game", Sent: 120000, Got: 119900, Gaps60: 50},
	}}
	if added, err := s.Finish(ctx, "ru-eurodir", task.ID, res); err != nil || added != 0 {
		t.Fatalf("added %d: %v", added, err)
	}
	if watch.loads != 1 || watch.finished != 0 || watch.slot != "ru-eurodir" || watch.entry != "ru-entry" || watch.load != spec || len(watch.res.Voice) != 2 {
		t.Fatalf("the watch got %+v", watch)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM stand_tasks WHERE id = $1`, id).Scan(&status); err != nil || status != TaskDone {
		t.Fatalf("status %q %v", status, err)
	}
	if _, err := s.QueueLoad(ctx, "ru-eurodir", "ru-entry", spec); err != nil {
		t.Fatalf("the next after the first is done: %v", err)
	}
}
