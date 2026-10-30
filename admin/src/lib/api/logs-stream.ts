import type { AdminLogEntry } from './logs';
import { fetchWSTicket } from './logs';
import { apiWsUrl } from '$lib/config/api';

// The REST client authenticates with cookies (credentials: 'include'); this
// socket uses a short-lived ticket fetched over REST instead, because a
// cross-origin WebSocket handshake is not a place to rely on cookie delivery.

export type LogStreamStatus = 'connecting' | 'open' | 'closed' | 'error';

export interface LogStreamFilter {
	level?: string[];
	source?: string;
	cat?: string[];
	userId?: string;
}

export interface LogStreamOptions {
	filter: LogStreamFilter;
	/** Optional JWT — used only if same-origin cookie auth is unavailable. */
	token?: string;
	onEvent: (ev: AdminLogEntry) => void;
	onStatus: (s: LogStreamStatus) => void;
}

function buildURL(filter: LogStreamFilter, token?: string): string {
	// Against the API's origin, not the panel's — see $lib/config/api.
	const url = new URL(apiWsUrl('/api/v1/admin/logs/stream'));
	if (filter.level?.length) url.searchParams.set('level', filter.level.join(','));
	if (filter.source) url.searchParams.set('source', filter.source);
	if (filter.cat?.length) url.searchParams.set('cat', filter.cat.join(','));
	if (filter.userId) url.searchParams.set('user_id', filter.userId);
	if (token) url.searchParams.set('token', token);
	return url.toString();
}

/**
 * Open a live WebSocket stream of app_logs events. Reconnects automatically with
 * exponential backoff (1s → 2s → 4s → ... capped at 30s) until the returned
 * cleanup function is invoked.
 */
export function openLogStream(opts: LogStreamOptions): () => void {
	let ws: WebSocket | null = null;
	let stopped = false;
	let retryMs = 1000;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

	function scheduleReconnect() {
		if (stopped) return;
		if (reconnectTimer) return;
		reconnectTimer = setTimeout(() => {
			reconnectTimer = null;
			connect();
		}, retryMs);
		retryMs = Math.min(retryMs * 2, 30_000);
	}

	async function connect() {
		if (stopped) return;
		opts.onStatus('connecting');

		// Prefer explicit token override (e.g. for tests); otherwise try the
		// short-lived ticket endpoint. If ticket fetch fails (e.g. backend not
		// redeployed with /ws-ticket yet), fall through to cookie-only auth so
		// legacy deploys still stream.
		let ticket = opts.token;
		if (!ticket) {
			try {
				ticket = await fetchWSTicket();
				console.info('[logs-stream] got ticket, opening WS');
			} catch (err) {
				console.warn('[logs-stream] ticket fetch failed, falling back to cookie auth:', err);
				ticket = undefined;
			}
		}

		if (stopped) return;

		try {
			const url = buildURL(opts.filter, ticket);
			console.info('[logs-stream] connecting to', url.replace(/token=[^&]+/, 'token=<redacted>'));
			ws = new WebSocket(url);
		} catch (err) {
			console.error('[logs-stream] construct failed:', err);
			opts.onStatus('error');
			scheduleReconnect();
			return;
		}

		let received = 0;

		ws.onopen = () => {
			retryMs = 1000; // reset backoff after a successful open
			opts.onStatus('open');
			console.info('[logs-stream] OPEN — waiting for events');
		};

		ws.onmessage = (e) => {
			try {
				const data = JSON.parse(e.data as string) as AdminLogEntry;
				received++;
				if (received === 1 || received % 50 === 0) {
					console.info(
						`[logs-stream] received ${received} events so far, latest: ${data.level}/${data.cat} "${data.msg}"`,
					);
				}
				opts.onEvent(data);
			} catch (err) {
				console.error('[logs-stream] parse error:', err);
			}
		};

		ws.onerror = (ev) => {
			console.error('[logs-stream] WS error event:', ev);
			opts.onStatus('error');
		};

		ws.onclose = (ev) => {
			console.warn(`[logs-stream] closed — code=${ev.code} reason="${ev.reason}" clean=${ev.wasClean}`);
			ws = null;
			if (stopped) {
				opts.onStatus('closed');
				return;
			}
			opts.onStatus('connecting');
			scheduleReconnect();
		};
	}

	connect();

	return function cleanup() {
		stopped = true;
		if (reconnectTimer) {
			clearTimeout(reconnectTimer);
			reconnectTimer = null;
		}
		if (ws) {
			try {
				ws.close();
			} catch {
				// no-op
			}
			ws = null;
		}
		opts.onStatus('closed');
	};
}
