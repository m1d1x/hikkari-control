// Where the admin API lives, as far as this panel is concerned.
//
// The panel is served from its OWN origin — a container today, adminus.<base>
// on its own host later — and never from the API's. So nothing in here may
// fall back to `window.location.origin`: that resolves to the static file
// server, and every call 404s (or worse, silently half-works while the dev
// server happens to proxy). One module owns the answer, which is the same
// no-hardcoded-domain rule landing/src/lib/config/domain.ts follows.
//
// Resolution order, most specific first:
//
//  1. `window.__NEKKO_ADMIN_CONFIG__.apiUrl` — written at CONTAINER START by
//     docker-entrypoint.sh from $NEKKO_API_URL. Runtime and not build time on
//     purpose: one image has to point at the local docker stack today and at
//     https://api.nekkochat.com from a rented box later, without a rebuild.
//  2. `VITE_API_URL` — build-time, for `npm run dev` against a stack that is
//     already up.
//  3. Empty string — same-origin. Kept only so a dev server behind a proxy
//     still works; it is not a configuration we deploy.

declare global {
	interface Window {
		__NEKKO_ADMIN_CONFIG__?: { apiUrl?: string };
	}
}

function resolveBase(): string {
	const injected = typeof window !== 'undefined' ? window.__NEKKO_ADMIN_CONFIG__?.apiUrl : undefined;
	const raw = (injected ?? (import.meta.env.VITE_API_URL as string | undefined) ?? '').trim();
	// Trailing slash would double up against the leading slash of every path.
	return raw.replace(/\/+$/, '');
}

/**
 * Origin the admin API is reached at, with no trailing slash. Empty means
 * same-origin.
 */
export const API_BASE_URL: string = resolveBase();

/**
 * Absolute URL for an API path (`/api/...`). Pass paths with a leading slash.
 */
export function apiUrl(path: string): string {
	return `${API_BASE_URL}${path}`;
}

/**
 * Same, as a `ws(s)://` URL — for the endpoints that upgrade.
 *
 * `base` may be relative (empty API_BASE_URL), so resolve against the page
 * origin FIRST and swap the scheme after: `new URL('ws://…')` on a relative
 * input throws, and a string replace on an origin-less path silently produces
 * garbage.
 */
export function apiWsUrl(path: string): string {
	const url = new URL(path, API_BASE_URL || window.location.origin);
	url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
	return url.toString();
}

/**
 * Resolve a URL the SERVER announced (realtime info hands out either an
 * absolute `wss://…` or a path-only `/ws`). A path-only announce is relative
 * to the API, never to this panel — the server means its own host.
 */
export function resolveAnnouncedWsUrl(announced: string): string {
	const url = new URL(announced, API_BASE_URL || window.location.origin);
	if (url.protocol === 'http:') url.protocol = 'ws:';
	else if (url.protocol === 'https:') url.protocol = 'wss:';
	return url.toString();
}
