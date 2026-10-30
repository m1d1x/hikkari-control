// Independent admin-panel login (no main-app session required).
//
// Two steps since task 77.2. The password goes to /api/auth/admin/login with
// the device binding; a right password answers with the second step, never
// with cookies. The second step — a passkey, a recovery code, or for an
// operator with no passkey yet the console code plus a first registration —
// is what makes the server set nekko_admin_access / nekko_admin_refresh.

import { getDeviceKey, bytesToBase64Url } from '$lib/crypto/device-key';
import { fingerprintHashBase64 } from '$lib/crypto/fingerprint';
import { apiUrl } from '$lib/config/api';
import { getAssertion, createCredential, defaultPasskeyName } from '$lib/auth/webauthn';

// Absolute, like every other call: the panel is served from its own origin,
// so an origin-relative path here would post the password to a static file
// server and read the 404 as "wrong credentials".
const LOGIN_URL = apiUrl('/api/auth/admin/login');
const LOGOUT_URL = apiUrl('/api/auth/admin/logout');
const CSRF_URL = apiUrl('/api/auth/admin/csrf');

// Read a cookie value, or '' when absent. The csrf cookie is deliberately not
// HttpOnly so this can read it back.
function readCookie(name: string): string {
	if (typeof document === 'undefined') return '';
	const prefix = name + '=';
	for (const part of document.cookie.split(';')) {
		const trimmed = part.trim();
		if (trimmed.startsWith(prefix)) return trimmed.slice(prefix.length);
	}
	return '';
}

/**
 * Fetch a double-submit token so the login POST can carry one.
 *
 * A cold panel holds no csrf cookie, and the server rejects a mutating request
 * without a header matching one. This GET mints the pair. Best-effort: if it
 * fails, login proceeds and the server's own rejection is what the user sees,
 * rather than a second error message about a step they did not ask for.
 */
async function primeCSRF(): Promise<string> {
	try {
		await fetch(CSRF_URL, { method: 'GET', credentials: 'include' });
	} catch {
		// fall through — read whatever is already there
	}
	return readCookie('nekko_admin_csrf');
}

export interface SignedIn {
	sessionId: string;
	user: { id: string; username: string; role: 'owner' | 'support' };
	/** Shown once, right after enrolment — the operator must save them. */
	recoveryCodes?: string[];
	/** After a recovery-code sign-in: how many are left. */
	recoveryCodesLeft?: number;
}

/**
 * What the password step answers (task 77.2). A right password never signs in
 * by itself: it opens a second step.
 */
export type LoginResult =
	| { kind: 'passkey'; ceremonyId: string; options: unknown }
	| { kind: 'enroll'; ceremonyId: string }
	| { kind: 'not-enrolled' }
	| { kind: 'invalid' }
	| { kind: 'forbidden' }
	| { kind: 'locked'; retryAfterSeconds: number }
	| { kind: 'expired' }
	| { kind: 'error'; message: string };

export type StepResult =
	| { kind: 'ok'; signedIn: SignedIn }
	| { kind: 'invalid' }
	| { kind: 'locked'; retryAfterSeconds: number }
	| { kind: 'expired' }
	| { kind: 'error'; message: string };

async function postAuth(url: string, body: unknown): Promise<Response> {
	const csrf = await primeCSRF();
	const headers: Record<string, string> = { 'Content-Type': 'application/json' };
	if (csrf) headers['X-CSRF-Token'] = csrf;
	return fetch(url, { method: 'POST', headers, credentials: 'include', body: JSON.stringify(body) });
}

function lockedFor(res: Response): number {
	const retry = parseInt(res.headers.get('Retry-After') || '900', 10);
	return Number.isFinite(retry) ? retry : 900;
}

async function errorOf(res: Response): Promise<string> {
	try {
		const b = await res.json();
		return typeof b?.error === 'string' ? b.error : '';
	} catch {
		return '';
	}
}

export async function loginAdmin(username: string, password: string): Promise<LoginResult> {
	const key = await getDeviceKey();
	const fp = await fingerprintHashBase64();
	let res: Response;
	try {
		res = await postAuth(LOGIN_URL, {
			username,
			password,
			device: {
				publicKey: bytesToBase64Url(key.publicKey),
				fingerprintHash: fp,
				platform: key.platform,
				isTrusted: key.isTrusted,
			},
		});
	} catch (e) {
		return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
	}

	if (res.ok) {
		const data = await res.json();
		if (data.step === 'passkey') return { kind: 'passkey', ceremonyId: data.ceremonyId, options: data.options };
		if (data.step === 'enroll') return { kind: 'enroll', ceremonyId: data.ceremonyId };
		return { kind: 'error', message: 'unexpected answer' };
	}
	if (res.status === 401) return { kind: 'invalid' };
	if (res.status === 403) {
		return (await errorOf(res)) === 'second_factor_not_enrolled' ? { kind: 'not-enrolled' } : { kind: 'forbidden' };
	}
	if (res.status === 423) return { kind: 'locked', retryAfterSeconds: lockedFor(res) };
	return { kind: 'error', message: `HTTP ${res.status}` };
}

async function finishStep(url: string, body: unknown): Promise<StepResult> {
	let res: Response;
	try {
		res = await postAuth(url, body);
	} catch (e) {
		return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
	}
	if (res.ok) return { kind: 'ok', signedIn: (await res.json()) as SignedIn };
	if (res.status === 401 || res.status === 400) return { kind: 'invalid' };
	if (res.status === 410) return { kind: 'expired' };
	if (res.status === 423) return { kind: 'locked', retryAfterSeconds: lockedFor(res) };
	return { kind: 'error', message: `HTTP ${res.status}` };
}

/** Second step with a passkey: Touch ID, then the server checks the answer. */
export async function loginWithPasskey(ceremonyId: string, options: unknown): Promise<StepResult> {
	const credential = await getAssertion(options);
	return finishStep(apiUrl('/api/auth/admin/login/passkey'), { ceremonyId, credential });
}

/** Second step with a recovery code, when no device with a passkey is at hand. */
export function loginWithRecoveryCode(ceremonyId: string, code: string): Promise<StepResult> {
	return finishStep(apiUrl('/api/auth/admin/login/recovery'), { ceremonyId, code });
}

/**
 * First passkey: the console code from `admin-create` / `admin-2fa-reset`,
 * then registration. Returns the session plus the recovery codes to show.
 */
export async function enrollFirstPasskey(ceremonyId: string, code: string): Promise<StepResult> {
	let res: Response;
	try {
		res = await postAuth(apiUrl('/api/auth/admin/enroll/begin'), { ceremonyId, code });
	} catch (e) {
		return { kind: 'error', message: e instanceof Error ? e.message : String(e) };
	}
	if (res.status === 401) return { kind: 'invalid' };
	if (res.status === 410) return { kind: 'expired' };
	if (res.status === 423) return { kind: 'locked', retryAfterSeconds: lockedFor(res) };
	if (!res.ok) return { kind: 'error', message: `HTTP ${res.status}` };
	const begin = await res.json();
	const credential = await createCredential(begin.options);
	return finishStep(apiUrl('/api/auth/admin/enroll/finish'), {
		ceremonyId: begin.ceremonyId,
		credential,
		name: defaultPasskeyName(),
	});
}

// logoutAdmin tells the server to revoke the current admin session and
// clear cookies. Safe to call even when not signed in (server treats it
// as a 401 which we ignore — UI just routes to /login regardless).
export async function logoutAdmin(): Promise<void> {
	try {
		await fetch(LOGOUT_URL, {
			method: 'POST',
			headers: { 'X-CSRF-Token': readCookie('nekko_admin_csrf') },
			credentials: 'include',
		});
	} catch {
		// best-effort; the redirect to /login happens regardless.
	}
}
