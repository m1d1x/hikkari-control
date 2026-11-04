package main

import (
	"context"

	"github.com/m1d1x/hikkari-control/internal/relayruns"
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
