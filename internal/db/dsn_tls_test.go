package db

// The gate on the Postgres transport rule (roadmap task 57.5).
//
// The rule itself is old; what 57.5 changed is WHERE it applies. It used to be
// a private function inside cmd/server — the binary whose database is usually
// on the same host — while cmd/sfu, the one that actually crosses a network,
// had no check at all. The prod EU shard reached the master
// through an ssh tunnel because nothing in the process ever demanded TLS.
//
// These cases pin the rule so the single shared copy cannot drift, and in
// particular so the loopback exception stays an exception: it is the one path
// that legitimately answers "no TLS needed", and widening it by accident would
// silently disarm the check for every remote host.

import "testing"

func TestRequireDSNTLSAcceptsExplicitTLSModes(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:p@db.internal:5432/nekko?sslmode=require",
		"postgres://u:p@db.internal:5432/nekko?sslmode=verify-ca",
		"postgres://u:p@db.internal:5432/nekko?sslmode=verify-full",
		"host=db.internal user=u sslmode=verify-full",
	} {
		if err := RequireDSNTLS(dsn); err != nil {
			t.Errorf("RequireDSNTLS(%q) = %v, want nil", dsn, err)
		}
	}
}

// The exception, and only for a host that cannot leave the machine.
func TestRequireDSNTLSAllowsDisableOnlyOnLoopback(t *testing.T) {
	ok := []string{
		"postgres://nekko:nekko@localhost:5432/nekko?sslmode=disable",
		"postgres://nekko:nekko@127.0.0.1:5432/nekko?sslmode=disable",
		"host=localhost user=nekko sslmode=disable",
	}
	for _, dsn := range ok {
		if err := RequireDSNTLS(dsn); err != nil {
			t.Errorf("RequireDSNTLS(%q) = %v, want nil (loopback exception)", dsn, err)
		}
	}

	// Every one of these is a real shape from this project's own history or
	// compose files. `postgres:5432` in particular is what the local stack
	// uses — a docker service name, NOT loopback — so it must be refused and
	// pass only through the explicit dev hatch.
	bad := []string{
		"postgres://nekko:nekko@postgres:5432/nekko?sslmode=disable",
		"postgres://nekko:nekko@10.0.0.5:5432/nekko?sslmode=disable",
		"postgres://nekko:nekko@db.example.com:5432/nekko?sslmode=disable",
		"host=db.example.com user=nekko sslmode=disable",
	}
	for _, dsn := range bad {
		if err := RequireDSNTLS(dsn); err == nil {
			t.Errorf("RequireDSNTLS(%q) = nil, want refusal — a remote host with TLS off", dsn)
		}
	}
}

// "localhost" as a prefix of something else is a different host entirely.
// Accepting it would turn the exception into a bypass anybody can spell.
func TestRequireDSNTLSDoesNotTreatLookalikeHostsAsLoopback(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:p@localhost.evil.com:5432/nekko?sslmode=disable",
		"postgres://u:p@notlocalhost:5432/nekko?sslmode=disable",
		"postgres://u:p@127.0.0.1.evil.com:5432/nekko?sslmode=disable",
	} {
		if err := RequireDSNTLS(dsn); err == nil {
			t.Errorf("RequireDSNTLS(%q) = nil — a lookalike host slipped through the loopback exception", dsn)
		}
	}
}

// Half-measures are refused explicitly rather than falling into the generic
// branch: `prefer` silently downgrades to plaintext when the server declines,
// which is the failure mode that looks configured and is not.
func TestRequireDSNTLSRefusesWeakModes(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:p@db.internal:5432/nekko?sslmode=prefer",
		"postgres://u:p@db.internal:5432/nekko?sslmode=allow",
	} {
		if err := RequireDSNTLS(dsn); err == nil {
			t.Errorf("RequireDSNTLS(%q) = nil, want refusal", dsn)
		}
	}
}

// An unspecified sslmode is refused, not guessed. libpq's own default is
// `prefer`, i.e. plaintext on request — the exact thing above is refused for.
func TestRequireDSNTLSRefusesUnspecified(t *testing.T) {
	if err := RequireDSNTLS("postgres://u:p@db.internal:5432/nekko"); err == nil {
		t.Error("a DSN with no sslmode was accepted — libpq would default to prefer, i.e. plaintext on request")
	}
}

// IPv6 loopback in URL form arrives bracketed. Failing to unwrap it would
// refuse a legitimately local database, which pushes an operator toward the
// dev escape hatch — and the hatch disables the check for every host, not
// just this one.
func TestRequireDSNTLSHandlesIPv6Loopback(t *testing.T) {
	if err := RequireDSNTLS("postgres://u:p@[::1]:5432/nekko?sslmode=disable"); err != nil {
		t.Errorf("IPv6 loopback refused: %v", err)
	}
	if err := RequireDSNTLS("postgres://u:p@[2001:db8::1]:5432/nekko?sslmode=disable"); err == nil {
		t.Error("a routable IPv6 address was accepted as loopback")
	}
}
