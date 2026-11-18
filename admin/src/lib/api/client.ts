import { writable } from 'svelte/store';
import { accessDenied } from '$lib/stores/access';
import { ensureFreshAdminAccess } from '$lib/auth/refresh';
import { apiUrl } from '$lib/config/api';
import { getAssertion } from '$lib/auth/webauthn';

/**
 * An API refusal the UI can name. `code` is the server's `error` field when it
 * sent one (`owner_required`, `step_up_required`, `last_passkey`, …).
 */
export class ApiError extends Error {
	constructor(
		readonly status: number,
		readonly code: string,
	) {
		super(`API error: ${status}${code ? ' ' + code : ''}`);
	}
}

/** Human text for the refusals an operator can do something about. */
export function describeApiError(e: unknown): string {
	if (e instanceof ApiError) {
		switch (e.code) {
			case 'owner_required':
				return 'нужна роль владельца';
			case 'step_up_required':
				return 'нужно подтвердить passkey';
			case 'last_passkey':
				return 'нельзя удалить последний passkey';
			case 'erasure already scheduled':
				return 'удаление уже запланировано';
			case 'password_too_short':
				return 'новый пароль короче 12 символов';
			case 'password_unchanged':
				return 'это и есть текущий пароль';
			case 'account_temporarily_locked':
				return 'слишком много ошибок, вход закрыт на 15 минут';
		}
		return e.message;
	}
	return e instanceof Error ? e.message : String(e);
}

async function errorCode(res: Response): Promise<string> {
	try {
		const body = await res.clone().json();
		return typeof body?.error === 'string' ? body.error : '';
	} catch {
		return '';
	}
}

/**
 * One passkey touch that authorizes destructive actions on this session for a
 * few minutes (task 77.3). Called by request() when the server answers
 * `step_up_required`, so the operator sees the Touch ID prompt in place of an
 * error and the action goes through.
 */
export async function stepUp(): Promise<void> {
	const begin = await request<{ ceremonyId: string; options: unknown }>('POST', '/api/v1/admin/security/step-up/begin');
	const credential = await getAssertion(begin.options);
	await request('POST', '/api/v1/admin/security/step-up/finish', { ceremonyId: begin.ceremonyId, credential });
}

/**
 * Absolute URL for a stored object, for the places that need one in markup
 * (an <img src>) rather than a fetch.
 *
 * Built against the same base every call above uses, deliberately: a
 * relative `/api/...` path would resolve against the ADMIN's origin, which is
 * not where the API lives. The server hands us a storage key precisely so the
 * origin is our decision and not baked into a value it minted.
 */
export function mediaUrl(key: string): string {
	return apiUrl(`/api/v1/admin/media/${key}`);
}

// Refresh endpoint itself: hitting refresh from inside refresh would loop.
const NO_REFRESH_PATHS = ['/api/auth/admin/refresh'];

function shouldRefreshOn401(path: string): boolean {
	for (const p of NO_REFRESH_PATHS) {
		if (path === p || path.startsWith(p + '?')) return false;
	}
	return true;
}

// Pull a single cookie value out of document.cookie. Returns '' if the
// cookie is missing — callers treat that as "no token yet" (e.g. before
// admin login). The csrf cookie is non-HttpOnly by design (server side
// uses HttpOnly: false) so JS can read it back here.
function readCookie(name: string): string {
	if (typeof document === 'undefined') return '';
	const prefix = name + '=';
	for (const part of document.cookie.split(';')) {
		const trimmed = part.trim();
		if (trimmed.startsWith(prefix)) return trimmed.slice(prefix.length);
	}
	return '';
}

// CSRF double-submit. Server sets nekko_admin_csrf alongside the access
// cookie on /api/auth/admin/login and rotates it on every refresh; we
// echo it back as X-CSRF-Token on every mutating request. Re-read per
// request (instead of caching) so a transparent refresh's new token
// replaces the old one without us having to subscribe to refresh events.
function isMutating(method: string): boolean {
	const m = method.toUpperCase();
	return m === 'POST' || m === 'PUT' || m === 'PATCH' || m === 'DELETE';
}

async function request<T>(
	method: string,
	path: string,
	body?: unknown,
	steppedUp = false,
	as: 'json' | 'text' = 'json',
): Promise<T> {
	const headers: Record<string, string> = {};
	if (body) headers['Content-Type'] = 'application/json';
	if (isMutating(method)) {
		const tok = readCookie('nekko_admin_csrf');
		if (tok) headers['X-CSRF-Token'] = tok;
	}
	const init: RequestInit = {
		method,
		headers: Object.keys(headers).length > 0 ? headers : undefined,
		credentials: 'include',
		body: body ? JSON.stringify(body) : undefined,
	};

	if (isMutating(method)) dropKeptAnswers();
	let res = await fetch(apiUrl(path), init);
	if (res.status === 401 && shouldRefreshOn401(path)) {
		try {
			await ensureFreshAdminAccess();
			res = await fetch(apiUrl(path), init);
		} catch {
			// fall through with the original 401 — accessDenied will fire below
		}
	}

	if (!res.ok) {
		const code = await errorCode(res);
		// A destructive action wants a fresh touch: take it and retry once.
		if (res.status === 403 && code === 'step_up_required' && !steppedUp) {
			await stepUp();
			return request<T>(method, path, body, true, as);
		}
		// A role refusal is an answer about this action, not about the session:
		// it must not throw the operator out to the login screen.
		if (res.status === 401 || (res.status === 403 && code !== 'owner_required' && code !== 'step_up_required')) {
			accessDenied.set(true);
		}
		throw new ApiError(res.status, code);
	}

	return as === 'text' ? ((await res.text()) as T) : res.json();
}

function withParams(path: string, params?: Record<string, string | number | undefined>): string {
	const url = new URL(path, window.location.origin);
	if (params) {
		Object.entries(params).forEach(([k, v]) => {
			if (v !== undefined) url.searchParams.set(k, String(v));
		});
	}
	return url.pathname + url.search;
}

// Every tab asks for its data on mount, and every answer crosses the SSH
// tunnel to the hub (a third of a second at best), so clicking between tabs
// meant looking at «загрузка» each time for data seen a minute ago.
//
// So a GET answer is kept for KEEP_MS and handed back at once on the next ask
// of the same URL. When it is older than FRESH_MS it is asked again behind the
// page; if the new answer differs, `freshAnswers` ticks and the page on
// screen reloads from the kept answer (onFreshAnswers). Any action (POST,
// PUT, PATCH, DELETE) drops everything kept: what it changed must show at
// once. Live screens (logs, jobs, security, the audit trail) are never kept.
const FRESH_MS = 10_000;
const KEEP_MS = 30 * 60_000;
const LIVE_PREFIXES = [
	'/api/v1/admin/support/tester-logs',
	'/api/v1/admin/entry/jobs',
	'/api/v1/admin/entry/hub-key',
	'/api/v1/admin/security',
	'/api/v1/admin/ws-ticket',
	'/api/v1/admin/logs',
	'/api/v1/admin/system/logs',
	'/api/v1/admin/trace',
	'/api/v1/admin/spans',
	'/api/v1/admin/audit',
];
const kept = new Map<string, { at: number; value: unknown }>();
const asking = new Map<string, Promise<unknown>>();

/** Ticks when an answer asked again behind a page came back different. */
export const freshAnswers = writable(0);
let tickQueued = false;

function isKept(url: string): boolean {
	if (!url.startsWith('/api/v1/admin/')) return false;
	return !LIVE_PREFIXES.some((p) => url === p || url.startsWith(p + '/') || url.startsWith(p + '?'));
}

/** Forget every kept answer: called on every action. */
export function dropKeptAnswers(): void {
	kept.clear();
	asking.clear();
	generation++;
}

// An answer asked for before an action must not be kept after it.
let generation = 0;

function ask<T>(url: string): Promise<T> {
	const inflight = asking.get(url);
	if (inflight) return inflight as Promise<T>;
	const gen = generation;
	const p = request<T>('GET', url)
		.then((value) => {
			if (gen !== generation) return value;
			const before = kept.get(url);
			kept.set(url, { at: Date.now(), value });
			if (before && JSON.stringify(before.value) !== JSON.stringify(value) && !tickQueued) {
				// Several answers landing together reload the page once.
				tickQueued = true;
				setTimeout(() => {
					tickQueued = false;
					freshAnswers.update((n) => n + 1);
				}, 50);
			}
			return value;
		})
		.finally(() => {
			if (asking.get(url) === p) asking.delete(url);
		});
	asking.set(url, p);
	return p;
}

function getKept<T>(url: string): Promise<T> {
	const k = kept.get(url);
	if (k && Date.now() - k.at < KEEP_MS) {
		if (Date.now() - k.at >= FRESH_MS) ask<T>(url).catch(() => {});
		return Promise.resolve(k.value as T);
	}
	return ask<T>(url);
}

export function get<T>(path: string, params?: Record<string, string | number | undefined>): Promise<T> {
	const url = withParams(path, params);
	return isKept(url) ? getKept<T>(url) : request<T>('GET', url);
}

/** A GET whose body is not JSON (an NDJSON export, a log file). */
export function getText(path: string, params?: Record<string, string | number | undefined>): Promise<string> {
	return request<string>('GET', withParams(path, params), undefined, false, 'text');
}

export function post<T>(path: string, body?: unknown): Promise<T> {
	return request<T>('POST', path, body);
}

export function put<T>(path: string, body?: unknown): Promise<T> {
	return request<T>('PUT', path, body);
}

export function del<T>(path: string): Promise<T> {
	return request<T>('DELETE', path);
}

export function patch<T>(path: string, body?: unknown): Promise<T> {
	return request<T>('PATCH', path, body);
}
