package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// runEntryFront is the operator's hand on the РФ-каскад (internal/entry/fronts.go):
//
//	entry-front attach <front> <exit-box> <address> <provider>  < root password
//	entry-front detach <front>
//
// attach logs in to a rented Russian machine, makes it forward the exit's
// relay ports, checks the exit's certificate answers through it, re-aims the
// exit's domain at it and re-signs the exit's relays with its address. It runs
// in the foreground; its lines are the hub log's. detach sends the exit's
// domain and records back to the exit.
func runEntryFront(args []string) {
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "entry-front: %v\n", err)
		os.Exit(1)
	}
	attach := len(args) == 5 && args[0] == "attach"
	detach := len(args) == 2 && args[0] == "detach"
	if !attach && !detach {
		fail(fmt.Errorf("usage: entry-front attach <front> <exit-box> <address> <provider> < password | entry-front detach <front>"))
	}
	if err := cryptobox.Configure(os.Getenv); err != nil {
		fail(fmt.Errorf("content key: %w", err))
	}
	signer, err := entry.LoadSigner(os.Getenv, entry.PinnedKeys())
	if err != nil {
		fail(err)
	}
	if signer == nil {
		fail(entry.ErrNoSigner)
	}
	_, cancel, pool := adminCLIPool()
	cancel() // the job outlives the CLI's 30 s connect budget
	defer pool.Close()
	svc, err := entry.NewService(pool, signer, entry.DefaultBatchSize)
	if err != nil {
		fail(err)
	}
	prov := newProvisioner(svc)
	ctx := context.Background()
	// The registrar the domain is re-aimed through.
	_ = wireEntryDomains(ctx, svc, prov)

	if detach {
		if err := prov.DetachFront(ctx, args[1]); err != nil {
			fail(err)
		}
		fmt.Printf("вход %s снят, домен и релеи вернулись на машину выхода\n", args[1])
		os.Exit(0)
	}
	password := ""
	if st, _ := os.Stdin.Stat(); st.Mode()&os.ModeCharDevice == 0 {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		password = strings.TrimRight(line, "\r\n")
	}
	f := entry.NewFront{Name: args[1], ExitBox: args[2], Address: args[3], Provider: args[4], Password: password}
	if err := prov.AttachFront(ctx, f); err != nil {
		fail(err)
	}
	fmt.Printf("вход %s стоит перед %s\n", f.Name, f.ExitBox)
	os.Exit(0)
}
