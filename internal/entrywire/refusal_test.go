package entrywire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/digitalocean"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/hetzner"
	"github.com/m1d1x/hikkari-control/internal/openstack"
	"github.com/m1d1x/hikkari-control/internal/porkbun"
	"github.com/m1d1x/hikkari-control/internal/vdsina"
	"github.com/m1d1x/hikkari-control/internal/vultr"
)

// A provider that said no charged nothing; one that said nothing may have.
// Only the first is a refusal (audit В3, В4): reading a lost answer as one
// takes a paid purchase out of the budget.
func TestOnlyAProvidersOwnNoIsARefusal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		refused bool
	}{
		{"porkbun funds", porkbun.ErrInsufficientFunds, true},
		{"porkbun taken", fmt.Errorf("wrap: %w", porkbun.ErrUnavailable), true},
		{"porkbun rate limit", porkbun.ErrRateLimited, true},
		{"porkbun said why", &porkbun.APIError{HTTPStatus: 400, Message: "Invalid domain."}, true},
		{"porkbun gateway page", &porkbun.APIError{HTTPStatus: 502}, false},
		{"porkbun 500 with words", &porkbun.APIError{HTTPStatus: 500, Message: "oops"}, false},
		{"porkbun timeout", fmt.Errorf("porkbun: http: %w", context.DeadlineExceeded), false},
	} {
		if got := errors.Is(registrarRefusal(tc.err), entry.ErrProviderRefused); got != tc.refused {
			t.Errorf("registrar %s: refused = %v, want %v", tc.name, got, tc.refused)
		}
	}
	for _, tc := range []struct {
		name    string
		err     error
		refused bool
	}{
		{"vultr capacity", vultr.ErrOutOfCapacity, true},
		{"vultr funds", vultr.ErrInsufficientFunds, true},
		{"vultr bad plan", &vultr.APIError{HTTPStatus: http.StatusBadRequest, Message: "Invalid plan"}, true},
		{"vultr 503", &vultr.APIError{HTTPStatus: http.StatusServiceUnavailable}, false},
		{"vultr 404 on an order", vultr.ErrNotFound, false},
		{"vultr 429 on an order", fmt.Errorf("wrap: %w", vultr.ErrRateLimited), false},
		{"vultr reset", errors.New("vultr: http: connection reset by peer"), false},

		{"hetzner capacity", hetzner.ErrOutOfCapacity, true},
		{"hetzner funds", hetzner.ErrInsufficientFunds, true},
		{"hetzner bad plan", &hetzner.APIError{HTTPStatus: http.StatusBadRequest, Message: "invalid plan"}, true},
		{"hetzner 503", &hetzner.APIError{HTTPStatus: http.StatusServiceUnavailable}, false},
		{"hetzner 404 on an order", hetzner.ErrNotFound, false},
		{"hetzner 429 on an order", fmt.Errorf("wrap: %w", hetzner.ErrRateLimited), false},
		{"hetzner reset", errors.New("hetzner: http: connection reset by peer"), false},

		{"digitalocean capacity", digitalocean.ErrOutOfCapacity, true},
		{"digitalocean funds", digitalocean.ErrInsufficientFunds, true},
		{"digitalocean bad plan", &digitalocean.APIError{HTTPStatus: http.StatusBadRequest, Message: "invalid size"}, true},
		{"digitalocean 503", &digitalocean.APIError{HTTPStatus: http.StatusServiceUnavailable}, false},
		{"digitalocean 404 on an order", digitalocean.ErrNotFound, false},
		{"digitalocean 429 on an order", fmt.Errorf("wrap: %w", digitalocean.ErrRateLimited), false},
		{"digitalocean reset", errors.New("digitalocean: http: connection reset by peer"), false},

		{"vdsina capacity", vdsina.ErrOutOfCapacity, true},
		{"vdsina funds", vdsina.ErrInsufficientFunds, true},
		{"vdsina bad plan", &vdsina.APIError{HTTPStatus: http.StatusBadRequest, Message: "invalid plan"}, true},
		{"vdsina 503", &vdsina.APIError{HTTPStatus: http.StatusServiceUnavailable}, false},
		{"vdsina 404 on an order", vdsina.ErrNotFound, false},
		{"vdsina 429 on an order", fmt.Errorf("wrap: %w", vdsina.ErrRateLimited), false},
		{"vdsina reset", errors.New("vdsina: http: connection reset by peer"), false},

		{"openstack capacity", openstack.ErrOutOfCapacity, true},
		{"openstack funds", openstack.ErrInsufficientFunds, true},
		{"openstack bad plan", &openstack.APIError{HTTPStatus: http.StatusBadRequest, Message: "flavor not found"}, true},
		{"openstack 503", &openstack.APIError{HTTPStatus: http.StatusServiceUnavailable}, false},
		{"openstack 404 on an order", openstack.ErrNotFound, false},
		{"openstack 429 on an order", fmt.Errorf("wrap: %w", openstack.ErrRateLimited), false},
		{"openstack reset", errors.New("openstack: http: connection reset by peer"), false},
	} {
		if got := errors.Is(hosterRefusal(tc.err), entry.ErrProviderRefused); got != tc.refused {
			t.Errorf("hoster %s: refused = %v, want %v", tc.name, got, tc.refused)
		}
	}
}

func TestRegionalCapacityRemainsADefiniteRefusal(t *testing.T) {
	for _, err := range []error{
		hosterRefusal(vultr.ErrOutOfCapacity),
		hosterRefusal(hetzner.ErrOutOfCapacity),
		hosterRefusal(digitalocean.ErrOutOfCapacity),
		hosterRefusal(vdsina.ErrOutOfCapacity),
		hosterRefusal(openstack.ErrOutOfCapacity),
	} {
		if !errors.Is(err, entry.ErrRegionCapacity) || !errors.Is(err, entry.ErrProviderRefused) {
			t.Fatalf("capacity classification lost: %v", err)
		}
	}
	if errors.Is(hosterRefusal(vultr.ErrInsufficientFunds), entry.ErrRegionCapacity) ||
		errors.Is(hosterRefusal(hetzner.ErrInsufficientFunds), entry.ErrRegionCapacity) ||
		errors.Is(hosterRefusal(digitalocean.ErrInsufficientFunds), entry.ErrRegionCapacity) ||
		errors.Is(hosterRefusal(vdsina.ErrInsufficientFunds), entry.ErrRegionCapacity) ||
		errors.Is(hosterRefusal(openstack.ErrInsufficientFunds), entry.ErrRegionCapacity) {
		t.Fatal("account credit cannot be solved by another region")
	}
}
