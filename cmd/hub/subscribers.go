package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/m1d1x/hikkari-control/internal/config"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

const gib = 1 << 30

// subscriber-add <name> <plan>: a new subscriber on plan's first period and
// their subscription link, printed once — the hub keeps only the token's hash.
//
// subscriber-renew <name> <plan>: the subscriber's next period on plan.
//
// subscriber-status <name>: the period's end and traffic.
//
// subscriber-revoke <name>: the link stops working at once.
//
// plan-set <id> <days> <traffic GiB, 0 for none> <price cents>: a plan on
// sale, or changed; subscriptions already sold keep their terms.
//
// plan-retire <id>: the plan is no longer sold.
func runSubscriber(cmd string, args []string) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, cmd+":", err)
		os.Exit(1)
	}
	want := map[string]int{
		"subscriber-add": 2, "subscriber-renew": 2, "subscriber-status": 1, "subscriber-revoke": 1,
		"plan-set": 4, "plan-retire": 1,
	}[cmd]
	if len(args) != want {
		fail(fmt.Errorf("wrong arguments, see cmd/hub/subscribers.go"))
	}
	ctx, cancel, pool := adminCLIPool()
	defer cancel()
	defer pool.Close()
	svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
	if err != nil {
		fail(err)
	}
	subs := subscription.New(pool, svc, entry.PinnedKeys())
	switch cmd {
	case "subscriber-add":
		token, err := subs.Add(ctx, args[0], args[1])
		if err != nil {
			fail(err)
		}
		fmt.Println(config.Default().SubURL() + "/sub/" + token)
	case "subscriber-renew":
		if err := subs.Renew(ctx, args[0], args[1]); err != nil {
			fail(err)
		}
		fmt.Println("renewed")
	case "subscriber-status":
		t, err := subs.Status(ctx, args[0])
		if err != nil {
			fail(err)
		}
		expires, limit := "never", "none"
		if !t.ExpiresAt.IsZero() {
			expires = t.ExpiresAt.Format(time.RFC3339)
		}
		if t.TrafficBytes > 0 {
			limit = fmt.Sprintf("%.2f GiB", float64(t.TrafficBytes)/gib)
		}
		fmt.Printf("plan %s, expires %s, used %.2f GiB of %s\n", t.Plan, expires, float64(t.Up+t.Down)/gib, limit)
	case "subscriber-revoke":
		if err := subs.Revoke(ctx, args[0]); err != nil {
			fail(err)
		}
		fmt.Println("revoked")
	case "plan-set":
		var n [3]int64
		for i, a := range args[1:] {
			if n[i], err = strconv.ParseInt(a, 10, 64); err != nil {
				fail(fmt.Errorf("%q is not a number", a))
			}
		}
		if err := subs.SetPlan(ctx, subscription.Plan{ID: args[0], Days: int(n[0]), TrafficBytes: n[1] * gib, PriceCents: int(n[2])}); err != nil {
			fail(err)
		}
		fmt.Println("plan set")
	case "plan-retire":
		if err := subs.RetirePlan(ctx, args[0]); err != nil {
			fail(err)
		}
		fmt.Println("plan retired")
	}
	os.Exit(0)
}
