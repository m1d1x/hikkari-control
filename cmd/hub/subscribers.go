package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
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
// subscriber-link <name>: the subscriber's link again, read back from the
// sealed token.
//
// subscriber-revoke <name>: the link stops working at once.
//
// plan-set <id> <days> <traffic GiB, 0 for none> <price kopecks> <title>: a
// plan on sale, or changed; subscriptions already sold keep their terms.
//
// plan-retire <id>: the plan is no longer sold.
//
// subscriber-cascade <name> <exit-box> <entry-box> <port>: the subscriber's
// links become the exit's Hikkari relays dialled at the trunk entry on
// entry-box, port (internal/subscription/cascade.go), and only them.
//
// subscriber-cascade-off <name>: the subscriber's links are its relays again.
// subscriber-countries <name> on|off: offer a cascade account NL, DE and Auto.
//
// subscriber-cascade-entry <name> <entry-box> <port>: a further entry of the
// subscriber's cascade (protocol v1.07): links for a client that dials
// several entries name it in entries=, and the client leaves a silent entry
// by itself. The entry's trunk must reach the cascade's exit.
//
// subscriber-cascade-entry-off <name> <entry-box>: the entry leaves the links.
func runSubscriber(cmd string, args []string) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, cmd+":", err)
		os.Exit(1)
	}
	want := map[string]int{
		"subscriber-add": 2, "subscriber-renew": 2, "subscriber-status": 1, "subscriber-link": 1, "subscriber-revoke": 1,
		"plan-set": 5, "plan-retire": 1, "subscriber-cascade": 4, "subscriber-cascade-off": 1,
		"subscriber-countries": 2, "subscriber-cascade-entry": 3, "subscriber-cascade-entry-off": 2,
	}[cmd]
	if len(args) != want {
		fail(fmt.Errorf("wrong arguments, see cmd/hub/subscribers.go"))
	}
	// Tokens are sealed with the content key, so the link can be shown
	// again (internal/subscription.URL).
	if err := cryptobox.Configure(os.Getenv); err != nil {
		fail(err)
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
		fmt.Println(subscription.LinkURL(token))
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
	case "subscriber-link":
		link, err := subs.URLOf(ctx, args[0])
		if err != nil {
			fail(err)
		}
		fmt.Println(link)
	case "subscriber-revoke":
		if err := subs.Revoke(ctx, args[0]); err != nil {
			fail(err)
		}
		fmt.Println("revoked")
	case "plan-set":
		var n [3]int64
		for i, a := range args[1:4] {
			if n[i], err = strconv.ParseInt(a, 10, 64); err != nil {
				fail(fmt.Errorf("%q is not a number", a))
			}
		}
		if err := subs.SetPlan(ctx, subscription.Plan{ID: args[0], Title: args[4], Days: int(n[0]), TrafficBytes: n[1] * gib, PriceCents: int(n[2])}); err != nil {
			fail(err)
		}
		fmt.Println("plan set")
	case "subscriber-cascade":
		port, err := strconv.Atoi(args[3])
		if err != nil {
			fail(fmt.Errorf("%q is not a port", args[3]))
		}
		if err := subs.SetCascade(ctx, args[0], args[1], args[2], port); err != nil {
			fail(err)
		}
		fmt.Printf("%s: hikkari relays of %s through %s:%d\n", args[0], args[1], args[2], port)
	case "subscriber-cascade-off":
		if err := subs.DropCascade(ctx, args[0]); err != nil {
			fail(err)
		}
		fmt.Println("cascade off")
	case "subscriber-countries":
		if args[1] != "on" && args[1] != "off" {
			fail(fmt.Errorf("usage: hub subscriber-countries <name> on|off"))
		}
		if err := subs.SetCountryProfiles(ctx, args[0], args[1] == "on"); err != nil {
			fail(err)
		}
		fmt.Printf("%s: countries %s\n", args[0], args[1])
	case "subscriber-cascade-entry":
		port, err := strconv.Atoi(args[2])
		if err != nil {
			fail(fmt.Errorf("%q is not a port", args[2]))
		}
		n, err := subs.SetCascadeEntry(ctx, args[0], args[1], port)
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s: entry %d is %s:%d\n", args[0], n, args[1], port)
	case "subscriber-cascade-entry-off":
		if err := subs.DropCascadeEntry(ctx, args[0], args[1]); err != nil {
			fail(err)
		}
		fmt.Printf("%s: %s off\n", args[0], args[1])
	case "plan-retire":
		if err := subs.RetirePlan(ctx, args[0]); err != nil {
			fail(err)
		}
		fmt.Println("plan retired")
	}
	os.Exit(0)
}
