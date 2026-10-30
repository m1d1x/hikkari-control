// Package config centralises runtime configuration that is shared across
// packages. domain.go owns the public-facing hostname surface — every place
// that needs to construct a user-visible URL (invite links, CORS origins,
// shareable post URLs, autoupdater feed, SFU signaling endpoints) goes
// through Domain so a domain rename never requires a code edit again.
package config

import (
	"os"
	"strings"
	"sync"
)

// Domain bundles every public hostname the product surfaces. Fields are
// hostnames (no scheme) — use the *URL helpers when you need a full URL.
//
// Default values are derived from PUBLIC_BASE_DOMAIN; individual subdomains
// can be overridden via their own env var when the convention doesn't fit
// (e.g. "adminus" instead of "admin").
type Domain struct {
	Base      string // e.g. "nekkochat.com" — landing / marketing root
	WebHost   string // e.g. "web.nekkochat.com" — browser app (chat) host
	APIHost   string // e.g. "api.nekkochat.com"
	WSHost    string // reserved for the future WS split — defaults to APIHost
	AdminHost string // e.g. "adminus.nekkochat.com"
	// Static update channel: the layout it must serve is defined by
	// client-core/nekko-updater (channel.rs) and produced by
	// `nekko-manifest publish` — see docs/UPDATE_CHANNEL.md. Go only ever
	// names this host (CSP, links); it never serves the channel, because an
	// update has to work when the API is down (roadmap 38.3).
	// ⚠️ Nothing is hosted here yet — no bucket exists.
	UpdateHost string
	CDNHost    string // static / uploads
	SFUHost    string // legacy single-region signaling host
}

// There was an SFURegions map here, region code → signaling URL, filled from
// SFU_REGION_<CODE>_URL env vars. It is gone rather than moved: «which shards
// exist» is answered by the registry (`sfu_nodes`, internal/sfureg), and this
// was the second answer to it (task 57, defect Д5).
//
// The two disagreed in both directions, and the CSP header is where it showed:
// it allowed hosts nobody had deployed while blocking the shards clients were
// actually routed to — which a browser reports as a dropped connection, not as
// a bad header. Its last two readers were the admin SFU views (now reading the
// registry) and the external prober (ARCHIVE, with the public edges it probed).

var (
	defaultOnce sync.Once
	defaultVal  Domain
)

// Default returns the process-wide Domain, lazily loaded from the env on
// first access. Cached for the lifetime of the process — env changes after
// boot are intentionally ignored to keep all subsystems consistent.
func Default() Domain {
	defaultOnce.Do(func() { defaultVal = Load() })
	return defaultVal
}

// Load reads env vars and constructs a fresh Domain. Tests use this
// directly; production code goes through Default().
func Load() Domain {
	base := envOr("PUBLIC_BASE_DOMAIN", "nekkochat.com")
	apiHost := envOr("PUBLIC_API_HOST", "api."+base)
	d := Domain{
		Base:       base,
		WebHost:    envOr("PUBLIC_WEB_HOST", "web."+base),
		APIHost:    apiHost,
		WSHost:     envOr("PUBLIC_WS_HOST", apiHost),
		AdminHost:  envOr("PUBLIC_ADMIN_HOST", "adminus."+base),
		UpdateHost: envOr("PUBLIC_UPDATE_HOST", "update."+base),
		CDNHost:    envOr("PUBLIC_CDN_HOST", "cdn."+base),
		SFUHost:    envOr("PUBLIC_SFU_HOST", "sfu."+base),
	}
	return d
}

// BaseURL returns "https://<Base>" — used for invite links and shareable
// post URLs that should always land on the user-facing host.
func (d Domain) BaseURL() string { return "https://" + d.Base }

// WebURL returns "https://<WebHost>" — the browser app (chat) entry point
// that the landing page links to and that auth flows land on.
func (d Domain) WebURL() string { return "https://" + d.WebHost }

// APIURL returns "https://<APIHost>".
func (d Domain) APIURL() string { return "https://" + d.APIHost }

// AdminURL returns "https://<AdminHost>".
func (d Domain) AdminURL() string { return "https://" + d.AdminHost }

// UpdateURL returns "https://<UpdateHost>".
func (d Domain) UpdateURL() string { return "https://" + d.UpdateHost }

// CDNURL returns "https://<CDNHost>".
func (d Domain) CDNURL() string { return "https://" + d.CDNHost }

// CookieDomain derives the parent domain that should scope cookies across
// all vhosts. For "nekkochat.com" it returns ".nekkochat.com"; for
// "dev.nekkochat.com" or "api.nekkochat.com" it also returns ".nekkochat.com"
// (the rightmost two labels). Localhost and IP literals are returned as-is
// so cookies stay scoped to the dev origin.
//
// The returned value is suitable for http.Cookie.Domain.
func (d Domain) CookieDomain(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return ""
	}
	// Strip port if present.
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	// IPv4/IPv6 literals: leave as-is, the Domain attribute is forbidden
	// for IP cookies and we'd rather skip it than emit something bogus.
	if isIPLiteral(host) {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	// Two-label domains ("nekkochat.com") become ".nekkochat.com".
	// Three+ label hosts ("api.nekkochat.com", "admin.dev.nekkochat.com")
	// reduce to the configured Base if the host is a subdomain of it,
	// otherwise to the rightmost two labels — this keeps the cookie
	// usable across all vhosts under the same site.
	if strings.HasSuffix(host, "."+d.Base) || host == d.Base {
		return "." + d.Base
	}
	return "." + strings.Join(parts[len(parts)-2:], ".")
}

// AllAdminOrigins returns every origin the main API should accept admin
// traffic from (production admin, staging admin, etc). Extra origins — the
// local `admin/` dev server included — come from PUBLIC_ADMIN_EXTRA_ORIGINS.
func (d Domain) AllAdminOrigins() []string {
	return append([]string{d.AdminURL()}, extraOrigins("PUBLIC_ADMIN_EXTRA_ORIGINS")...)
}

// AllWebOrigins returns every origin a user-facing browser surface of ours is
// served from: the apex (the landing, landing/), its www alias, and the web app
// host (Flutter Web, once it ships). These are separate origins from the API,
// so a plain fetch from any of them is cross-origin and needs an explicit
// Access-Control-Allow-Origin — without it the landing's ideas feed
// (landing/src/lib/api/http.ts) gets a 200 the browser refuses to hand over.
//
// The www alias is here even though the edge redirects it: a redirect does not
// survive a preflight, and an XHR that starts on www would fail at the OPTIONS
// rather than following the 301.
//
// Extra origins — the `landing/` dev server included — come from
// PUBLIC_WEB_EXTRA_ORIGINS.
func (d Domain) AllWebOrigins() []string {
	out := []string{d.BaseURL(), "https://www." + d.Base, d.WebURL()}
	return append(out, extraOrigins("PUBLIC_WEB_EXTRA_ORIGINS")...)
}

// AllBrowserOrigins returns every origin the API answers cross-origin requests
// from, deduplicated: the user-facing surfaces plus the admin panel. This is the
// CORS allowlist — one list, so a new browser surface is added in one place
// instead of being discovered as a blocked fetch.
func (d Domain) AllBrowserOrigins() []string {
	seen := map[string]bool{}
	out := make([]string, 0, 6)
	for _, o := range append(d.AllWebOrigins(), d.AllAdminOrigins()...) {
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return out
}

// extraOrigins reads a comma-separated origin list from an env var, skipping
// empty entries.
func extraOrigins(envVar string) []string {
	raw := os.Getenv(envVar)
	if raw == "" {
		return nil
	}
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func isIPLiteral(host string) bool {
	if strings.ContainsAny(host, ":") {
		return true // IPv6
	}
	dots := 0
	for _, c := range host {
		switch {
		case c >= '0' && c <= '9':
		case c == '.':
			dots++
		default:
			return false
		}
	}
	return dots == 3
}
