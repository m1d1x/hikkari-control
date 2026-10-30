package entry

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOnlyADefiniteRegionalCapacityRefusalMovesAnAutomaticOrder(t *testing.T) {
	capacity := fmt.Errorf("%w: %w", ErrProviderRefused, ErrRegionCapacity)
	for _, tc := range []struct {
		name      string
		cause     error
		explicit  bool
		geography bool
		wantCalls int
	}{
		{"capacity", capacity, false, true, 2},
		{"timeout", context.DeadlineExceeded, false, true, 1},
		{"credit", ErrProviderRefused, false, true, 1},
		{"explicit region", capacity, true, true, 1},
		{"unknown geography", capacity, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHostHarness(t)
			continent := ""
			if tc.geography {
				continent = "EU"
			}
			h.host.regions = []BoxRegion{{ID: "r1", City: "First", Country: "NL", Continent: continent}, {ID: "r2", City: "Second", Country: "DE", Continent: "EU"}, {ID: "r3", City: "Far", Country: "JP", Continent: "AS"}}
			h.host.plans[0].Regions = []string{"r1", "r2", "r3"}
			h.host.createErrForRegion = map[string]error{"r1": tc.cause}
			spec := OrderSpec{}
			if tc.explicit {
				spec.Region = "r1"
			}
			_, pub, err := h.svc.HubSSHKey(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got, err := h.svc.order(context.Background(), spec, pub)
			if len(h.host.attempted) != tc.wantCalls {
				t.Fatalf("calls %d, want %d: %v", len(h.host.attempted), tc.wantCalls, err)
			}
			if tc.wantCalls == 2 {
				if err != nil || got.instance.Region != "r2" {
					t.Fatalf("same-price European alternative: %+v, %v", got, err)
				}
				var refused, created int
				if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE status='refused'),count(*) FILTER (WHERE status='created') FROM entry_box_orders`).Scan(&refused, &created); err != nil {
					t.Fatal(err)
				}
				if refused != 1 || created != 1 {
					t.Fatalf("reservation settlement: refused=%d created=%d", refused, created)
				}
			} else if !errors.Is(err, tc.cause) {
				t.Fatalf("cause lost: %v", err)
			}
		})
	}
}
