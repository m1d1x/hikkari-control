package entrywire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/porkbun"
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
		// Audit 42 recheck НН7: neither says the order was looked at. A
		// 404 on POST /instances is a route or a proxy, a 429 a limiter in
		// front of whatever may have run — kept in the budget until the
		// listing says no machine came of it.
		{"vultr 404 on an order", vultr.ErrNotFound, false},
		{"vultr 429 on an order", fmt.Errorf("wrap: %w", vultr.ErrRateLimited), false},
		{"vultr reset", errors.New("vultr: http: connection reset by peer"), false},
	} {
		if got := errors.Is(hosterRefusal(tc.err), entry.ErrProviderRefused); got != tc.refused {
			t.Errorf("hoster %s: refused = %v, want %v", tc.name, got, tc.refused)
		}
	}
}

func TestRegionalCapacityRemainsADefiniteRefusal(t *testing.T) {
	err := hosterRefusal(vultr.ErrOutOfCapacity)
	if !errors.Is(err, entry.ErrRegionCapacity) || !errors.Is(err, entry.ErrProviderRefused) {
		t.Fatalf("capacity classification lost: %v", err)
	}
	if errors.Is(hosterRefusal(vultr.ErrInsufficientFunds), entry.ErrRegionCapacity) {
		t.Fatal("account credit cannot be solved by another region")
	}
}
