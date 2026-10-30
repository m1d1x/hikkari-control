package main

import (
	"github.com/m1d1x/hikkari-control/internal/admin"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Audit 42 В5: both packages must compose on the same mux with analytics.
func TestFullAdminMuxRegistersWithoutConflicts(t *testing.T) {
	mux := http.NewServeMux()
	gate := func(h http.HandlerFunc) http.HandlerFunc { return h }
	admin.RegisterRoutes(mux, admin.NewHandlers(nil, nil), &auth.KeySet{}, nil)
	entry.RegisterAdminRoutes(mux, entry.NewAdminHandlers(nil, nil, nil), gate, gate)
	for _, path := range []string{"/api/v1/admin/entry/machines", "/api/v1/admin/entry/fleet"} {
		_, pattern := mux.Handler(&http.Request{Method: "GET", URL: &url.URL{Path: path}})
		if pattern == "" {
			t.Fatalf("route missing: %s", path)
		}
	}
}
func TestWeakPulseMasterIsRejectedAtStartup(t *testing.T) {
	for _, token := range []string{"", "short", strings.Repeat("x", 32)} {
		err := validateFleetSecrets(func(string) string { return token })
		if (token == "short") != (err != nil) {
			t.Fatalf("token length %d: %v", len(token), err)
		}
	}
}
