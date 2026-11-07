package entry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The hello tap is a box setting (migration 040): on, the box's next install
// writes EDGE_HELLO_TAP=1 into its Reality relays' env, and Reconfigure runs
// that install now on the build the box has — drained, probed, back in the pool.
func TestHelloTapReachesTheRelayEnvThroughOneReconfigure(t *testing.T) {
	rh := newRolloutHarness(t)
	ctx := context.Background()
	box := rh.boxOnV1()
	o := rh.orchestrator()
	envOf := func() string {
		fb := rh.fleet.box(t, rh.harness, box)
		fb.mu.Lock()
		defer fb.mu.Unlock()
		// The last relay env the box was given.
		var env string
		for _, in := range fb.stdins {
			if strings.Contains(in, "EDGE_PRIVATE_KEY=") {
				env = in
			}
		}
		return env
	}
	if strings.Contains(envOf(), "EDGE_HELLO_TAP") {
		t.Fatal("the tap is on before anyone turned it on")
	}
	if err := rh.svc.SetHelloTap(ctx, box, true); err != nil {
		t.Fatal(err)
	}
	if err := o.Reconfigure(ctx, box); err != nil {
		t.Fatalf("update: %v", err)
	}
	if env := envOf(); !strings.Contains(env, "EDGE_HELLO_TAP=1\n") {
		t.Fatalf("env after the update:\n%s", env)
	}
	if got := rh.recorded(box); got != rh.sha1 {
		t.Fatalf("the box runs %s, want the build it had %s", got, rh.sha1)
	}
	for _, s := range rh.statuses(box) {
		if s != StatusAlive {
			t.Fatalf("relays after the update: %v", rh.statuses(box))
		}
	}
	if err := rh.svc.SetHelloTap(ctx, box, false); err != nil {
		t.Fatal(err)
	}
	if err := o.Reconfigure(ctx, box); err != nil {
		t.Fatalf("update off: %v", err)
	}
	if strings.Contains(envOf(), "EDGE_HELLO_TAP") {
		t.Fatal("the tap stays on after it was turned off")
	}
	if err := rh.svc.SetHelloTap(ctx, "no-such-box", true); !errors.Is(err, ErrNoSuchBox) {
		t.Fatalf("unknown box: %v", err)
	}
}

// The tunnel's location lets a stream-up upload run as long and as large as
// the stream: nginx's default 1m body cap cut every connection after 1 MiB
// up on euw1 (06.11-07.11, bench upload 0 and half the media transfers lost).
func TestTheXhttpFrontLetsTheUploadStreamRun(t *testing.T) {
	loc := frontScript[strings.Index(frontScript, `location ~ "^$secret`):]
	loc = loc[:strings.Index(loc, "}")]
	for _, want := range []string{"client_max_body_size 0;", "client_body_timeout 1h;", "proxy_request_buffering off;"} {
		if !strings.Contains(loc, want) {
			t.Errorf("the tunnel location lacks %q:\n%s", want, loc)
		}
	}
}
