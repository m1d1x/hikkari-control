package main

import (
	"context"

	"github.com/m1d1x/hikkari-control/internal/relayruns"
	"github.com/m1d1x/hikkari-control/internal/subscription"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// testerAliases names a tester's install on the relay chart the way every
// other screen does: by its alias, so a rename shows on its old rows too.
func testerAliases(store support.TesterStore) relayruns.Namer {
	return func(ctx context.Context) (func(string) string, error) {
		installs, err := store.ListInstalls(ctx)
		if err != nil {
			return nil, err
		}
		names := make(map[string]string, len(installs))
		for _, in := range installs {
			if in.Alias != "" {
				names[in.InstallID] = in.Alias
			}
		}
		return func(id string) string {
			if a, ok := names[id]; ok {
				return a
			}
			return id
		}, nil
	}
}

// standFleet is the fleet as the stand measures it: every relay a subscriber
// can be handed, rendered by the subscription service with the relay's own
// credential.
type standFleet struct {
	subs  *subscription.Service
	fleet subscription.Fleet
}

func (f standFleet) Links(ctx context.Context) ([]string, error) {
	return f.subs.FleetLinks(ctx, f.fleet)
}

func (f standFleet) CapacityLinks(ctx context.Context, slot string) ([]string, error) {
	return f.subs.CapacityFleetLinks(ctx, f.fleet, slot)
}
func (f standFleet) PrepareCapacityPeers(ctx context.Context) error {
	return f.subs.PrepareCapacityPeers(ctx)
}
