package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/entrywire"
)

// runReceiverCert is the owner's hand on the receiver group's certificate
// (hikkari-client plans/2026-11-16-second-receiver.md, P1):
//
//	receiver-cert                          the kept certificates
//	receiver-cert issue <domain> [staging] an order over DNS-01; staging
//	                                       is thrown away, never served
//	receiver-cert roll <domain>            every box of the group onto the
//	                                       kept one (reconfigure, hitless)
//
// The hub renews by itself a month before expiry and rolls the group after.
func runReceiverCert(args []string) {
	fail := func(err error) { fmt.Fprintf(os.Stderr, "receiver-cert: %v\n", err); os.Exit(1) }
	usage := fmt.Errorf("usage: receiver-cert | receiver-cert issue <domain> [staging] | receiver-cert roll <domain>")
	issue := len(args) >= 2 && len(args) <= 3 && args[0] == "issue" && (len(args) == 2 || args[2] == "staging")
	roll := len(args) == 2 && args[0] == "roll"
	if len(args) != 0 && !issue && !roll {
		fail(usage)
	}
	if err := cryptobox.Configure(os.Getenv); err != nil {
		fail(fmt.Errorf("content key: %w", err))
	}
	_, cancel, pool := adminCLIPool()
	cancel() // an order waits for DNS for minutes, a roll drains
	defer pool.Close()
	svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
	if err != nil {
		fail(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 40*time.Minute)
	defer stop()
	switch {
	case issue:
		svc.SetProviderConnector(entrywire.Providers())
		if err := svc.LoadProviders(ctx); err != nil {
			fail(fmt.Errorf("registrar: %w", err))
		}
		got, err := svc.IssueReceiverCert(ctx, args[1], len(args) == 3)
		if err != nil {
			fail(err)
		}
		where := "kept; receiver-cert roll " + got.Domain + " puts it on the receivers"
		if !got.Kept {
			where = "staging, thrown away"
		}
		fmt.Printf("%s: issued by %s, valid to %s, %s\n", got.Domain, got.Directory, got.NotAfter.UTC().Format(time.RFC3339), where)
	case roll:
		boxes, err := svc.ReceiverBoxes(ctx, args[1])
		if err != nil {
			fail(err)
		}
		if len(boxes) == 0 {
			fail(fmt.Errorf("no live box has a Hikkari relay on %s", args[1]))
		}
		o := entry.NewOrchestrator(svc, newProvisioner(svc))
		for _, b := range boxes {
			if err := o.Reconfigure(ctx, b); err != nil {
				fail(fmt.Errorf("reconfigure %s: %w", b, err))
			}
			fmt.Printf("%s reconfigured\n", b)
		}
	default:
		certs, err := svc.ReceiverCerts(ctx)
		if err != nil {
			fail(err)
		}
		for _, c := range certs {
			fmt.Printf("%s\tvalid to %s\tissued %s\t%s\n", c.Domain, c.NotAfter.UTC().Format(time.RFC3339), c.IssuedAt.UTC().Format(time.RFC3339), c.Directory)
		}
	}
	os.Exit(0)
}
