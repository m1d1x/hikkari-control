package main

import (
	"fmt"
	"os"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/entrywire"
)

func runDomainA(args []string) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "domain-a:", err)
		os.Exit(1)
	}
	if len(args) != 2 {
		fail(fmt.Errorf("usage: domain-a <subdomain> <ipv4>"))
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
	svc.SetProviderConnector(entrywire.Providers())
	if err := svc.LoadProviders(ctx); err != nil {
		fail(fmt.Errorf("registrar: %w", err))
	}
	id, err := svc.EnsureSubdomainA(ctx, args[0], args[1])
	if err != nil {
		fail(err)
	}
	fmt.Printf("%s A %s (record %s)\n", args[0], args[1], id)
}
