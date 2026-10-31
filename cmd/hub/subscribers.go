package main

import (
	"fmt"
	"os"

	"github.com/m1d1x/hikkari-control/internal/config"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

// subscriber-add <name>: a new subscriber and their subscription link, printed
// once — the hub keeps only the token's hash.
//
// subscriber-revoke <name>: the link stops working at once.
func runSubscriber(cmd string, args []string) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, cmd+":", err)
		os.Exit(1)
	}
	if len(args) != 1 {
		fail(fmt.Errorf("usage: hub %s <name>", cmd))
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
		token, err := subs.Add(ctx, args[0])
		if err != nil {
			fail(err)
		}
		fmt.Println(config.Default().SubURL() + "/sub/" + token)
	case "subscriber-revoke":
		if err := subs.Revoke(ctx, args[0]); err != nil {
			fail(err)
		}
		fmt.Println("revoked")
	}
	os.Exit(0)
}
