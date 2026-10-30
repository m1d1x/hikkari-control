package main

import (
	"context"
	"log"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/entrywire"
)

// wireEntryDomains gives the hub what it needs to run a relay's throwaway
// domain by itself (roadmap 42 П4в Ф6), and returns the repairer that carries
// it out (П9.6).
//
// The replacement of a blocked domain is wired always: even a hub with no
// registrar can cure a domain_blocked death by claiming another name from its
// seeded pool. What it is wired to is the repairer and not the provisioner
// directly, so the replacement goes through its playbook's mode — the owner's
// rule of 22.10 is that a repair which spends money waits for a person, and
// buying a domain is the one automatic repair that did not, before П9.6.
//
// The registrar — the ability to buy when the pool is dry — is the account the
// owner saved in the console (entry_providers, П9.1), loaded here and swapped
// live on every save. With none saved the hub is pool-only: it serves the names
// it already owns and never buys.
func wireEntryDomains(ctx context.Context, svc *entry.Service, prov *entry.Provisioner) *entry.Mechanic {
	mech := entry.NewMechanic(svc, prov)
	svc.SetDomainReplacer(mech)
	svc.SetProviderConnector(entrywire.Providers())
	if err := svc.LoadProviders(ctx); err != nil {
		log.Printf("entry: load provider accounts: %v — domain pool only, no purchases", err)
	}
	// The money the console shows is the hub's own reading, refreshed by the
	// hub: nobody presses "check" for the autopilot.
	go svc.RunProviderChecks(ctx, entry.ProviderCheckInterval)
	return mech
}
