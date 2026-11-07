package entry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The hello tap is a box setting (migration 040): on, the box's next install
// writes EDGE_HELLO_TAP=1 into its Reality relays' env, and UpdateBox runs
// that install now — drained, the image's build, probed, back in the pool.
func TestHelloTapReachesTheRelayEnvThroughOneBoxUpdate(t *testing.T) {
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
	if err := o.UpdateBox(ctx, box); err != nil {
		t.Fatalf("update: %v", err)
	}
	if env := envOf(); !strings.Contains(env, "EDGE_HELLO_TAP=1\n") {
		t.Fatalf("env after the update:\n%s", env)
	}
	if got := rh.recorded(box); got != rh.sha2 {
		t.Fatalf("the box runs %s, want the image's %s", got, rh.sha2)
	}
	for _, s := range rh.statuses(box) {
		if s != StatusAlive {
			t.Fatalf("relays after the update: %v", rh.statuses(box))
		}
	}
	if err := rh.svc.SetHelloTap(ctx, box, false); err != nil {
		t.Fatal(err)
	}
	if err := o.UpdateBox(ctx, box); err != nil {
		t.Fatalf("update off: %v", err)
	}
	if strings.Contains(envOf(), "EDGE_HELLO_TAP") {
		t.Fatal("the tap stays on after it was turned off")
	}
	if err := rh.svc.SetHelloTap(ctx, "no-such-box", true); !errors.Is(err, ErrNoSuchBox) {
		t.Fatalf("unknown box: %v", err)
	}
}
