// Proactive admin-session keepalive. Calls /api/auth/admin/refresh on a
// timer (well inside the 15-min access TTL) plus on tab visibility
// transitions, so an admin who leaves the panel open in a quiet tab
// never falls off the sliding 60-day refresh window.

import { ensureFreshAdminAccess } from './refresh';

// 10 min — comfortable margin under AccessTTL (15 min). If we miss one
// tick due to suspension/throttling we still have a 5-min cushion to
// catch the next focus event before access expires.
const REFRESH_INTERVAL_MS = 10 * 60 * 1000;

// On focus, re-refresh only if the last successful refresh is older
// than this. Avoids a thundering refresh every time the user alt-tabs.
const FOCUS_REFRESH_AGE_MS = 5 * 60 * 1000;

let timer: ReturnType<typeof setInterval> | null = null;
let lastRefreshAt = 0;
let onVisibility: (() => void) | null = null;
let onFocus: (() => void) | null = null;

async function tick() {
	try {
		await ensureFreshAdminAccess();
		lastRefreshAt = Date.now();
	} catch {
		// Swallow — next API call will surface 401 and the app's
		// accessDenied path takes over. Keepalive failures don't need
		// their own UI.
	}
}

export function startKeepalive() {
	if (timer) return;
	lastRefreshAt = Date.now();
	timer = setInterval(tick, REFRESH_INTERVAL_MS);
	onVisibility = () => {
		if (document.visibilityState !== 'visible') return;
		if (Date.now() - lastRefreshAt < FOCUS_REFRESH_AGE_MS) return;
		void tick();
	};
	onFocus = () => {
		if (Date.now() - lastRefreshAt < FOCUS_REFRESH_AGE_MS) return;
		void tick();
	};
	document.addEventListener('visibilitychange', onVisibility);
	window.addEventListener('focus', onFocus);
}

export function stopKeepalive() {
	if (timer) {
		clearInterval(timer);
		timer = null;
	}
	if (onVisibility) {
		document.removeEventListener('visibilitychange', onVisibility);
		onVisibility = null;
	}
	if (onFocus) {
		window.removeEventListener('focus', onFocus);
		onFocus = null;
	}
}
