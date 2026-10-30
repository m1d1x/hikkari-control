package db

// The transport rule for every Postgres connection this project opens.
//
// It lived inside cmd/server until task 57.5, which is exactly backwards from
// where it was needed: cmd/server talks to a database that is usually on the
// same host, while cmd/sfu is the one that crosses the network — a shard
// reaching the master from another region. That one had NO check at all. The
// prod EU shard reached Postgres through an
// ssh tunnel precisely because nothing in the code ever demanded TLS; ssh was
// standing in for a transport guarantee the process should have required
// itself.
//
// One rule, three callers, no per-binary interpretation of what "secure
// enough" means.

import (
	"errors"
	"log"
	"net"
	"os"
	"strings"
)

// InsecureDBEnvVar is the single escape hatch, spelled the same everywhere.
// A per-binary variant would let one process quietly relax a rule the others
// enforce.
const InsecureDBEnvVar = "NEKKO_DEV_ALLOW_INSECURE_DB"

// RequireSecureDSNOrFatal applies the rule and ends the process when the DSN
// is unacceptable, mirroring envcontract.ValidateOrFatal: a transport problem is a
// startup problem, not something to discover on the first query.
//
// binary names the caller so an operator reading a crash loop knows WHICH
// process refused, which matters when three of them share one env file.
func RequireSecureDSNOrFatal(binary, connStr string) {
	err := RequireDSNTLS(connStr)
	if err == nil {
		return
	}
	if os.Getenv(InsecureDBEnvVar) != "1" {
		log.Fatalf("%s: DATABASE_URL refused: %v (set %s=1 ONLY for local dev)", binary, err, InsecureDBEnvVar)
	}
	log.Printf("WARN: %s: DATABASE_URL has %v — allowed only because %s=1", binary, err, InsecureDBEnvVar)
}

// RequireDSNTLS rejects a Postgres connection string that disables TLS in
// production. We only allow sslmode=disable when the database lives on the
// same host as the server (localhost/127.0.0.1) — loopback traffic does not
// leave the machine, so 152-ФЗ ст.19 (ПДн в транзите) is moot. Any remote
// host MUST use sslmode=require or stronger.
//
// Accepted modes: require | verify-ca | verify-full. Disable is allowed
// only for loopback. Anything else returns a non-nil error describing the
// problem; callers may bypass by setting NEKKO_DEV_ALLOW_INSECURE_DB=1
// explicitly.
func RequireDSNTLS(connStr string) error {
	lower := strings.ToLower(connStr)
	if strings.Contains(lower, "sslmode=verify-full") ||
		strings.Contains(lower, "sslmode=verify-ca") ||
		strings.Contains(lower, "sslmode=require") {
		return nil
	}
	if strings.Contains(lower, "sslmode=disable") {
		// Loopback exception: Postgres on the same host. Match the host
		// segment between "@" and "/" or ":" to detect localhost. URL-form
		// connection strings dominate; key=value form is rare in this
		// codebase but covered too.
		if isLoopbackDBHost(connStr) {
			return nil
		}
		return errors.New("sslmode=disable allowed only for loopback Postgres (localhost/127.0.0.1)")
	}
	if strings.Contains(lower, "sslmode=allow") || strings.Contains(lower, "sslmode=prefer") {
		return errors.New("sslmode=allow/prefer is not strict enough — use require or stricter")
	}
	return errors.New("DATABASE_URL must explicitly set sslmode=require, verify-ca, or verify-full")
}

// isLoopbackDBHost extracts the host from a Postgres connection string and
// reports whether it is genuinely loopback. Unknown shapes return false, i.e.
// fail closed and demand TLS.
//
// The address is parsed, not prefix-matched. The lifted version tested
// `strings.HasPrefix(host, "127.")`, which accepts `127.0.0.1.evil.com` — a
// perfectly ordinary DNS name that resolves wherever its owner likes — as
// loopback, and with it `sslmode=disable` to a remote host. The gRPC side of
// this same question (internal/sfu/grpcclient.isLoopbackAddr) already used
// net.ParseIP; the two now agree.
func isLoopbackDBHost(connStr string) bool {
	// URL form: postgres://user:pass@host:port/db?...
	if i := strings.Index(connStr, "@"); i >= 0 {
		rest := connStr[i+1:]
		// A bracketed IPv6 literal is taken whole: splitting on the first
		// colon would cut "[::1]:5432" down to "[", and a legitimately local
		// database would be refused. That matters more than it looks — the
		// operator's next move is the dev escape hatch, which turns the check
		// off for EVERY host, not just this one.
		if strings.HasPrefix(rest, "[") {
			if j := strings.Index(rest, "]"); j > 0 {
				return isLoopbackHostname(rest[1:j])
			}
			return false
		}
		end := len(rest)
		for _, c := range []string{"/", "?", ":"} {
			if j := strings.Index(rest, c); j >= 0 && j < end {
				end = j
			}
		}
		return isLoopbackHostname(rest[:end])
	}
	// key=value form: ... host=localhost ...
	for _, kv := range strings.Fields(connStr) {
		if strings.HasPrefix(kv, "host=") {
			return isLoopbackHostname(strings.TrimPrefix(kv, "host="))
		}
	}
	return false
}

// isLoopbackHostname reports whether host is the literal name "localhost" or
// an IP literal inside a loopback range. Anything else — including a DNS name
// that merely starts with "127." or "localhost" — is a remote host, because
// what it resolves to is decided by whoever controls the name, not by us.
func isLoopbackHostname(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.Trim(host, "[]") // IPv6 literal in URL form
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
