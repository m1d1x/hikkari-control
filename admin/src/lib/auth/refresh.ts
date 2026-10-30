// Single-flight admin refresh. Same protocol as the app's client refresh (the
// Svelte refresh.ts is archived at ARCHIVE/legacy-frontend/src/lib/api/; the
// native app does it in Rust client-core), but hits /api/auth/admin/refresh
// and signs nonces with the admin device key.

import { getDeviceKey, bytesToBase64Url, base64UrlToBytes } from '$lib/crypto/device-key';
import { apiUrl } from '$lib/config/api';

const REFRESH_URL = apiUrl('/api/auth/admin/refresh');

let inflight: Promise<void> | null = null;

export function ensureFreshAdminAccess(): Promise<void> {
	if (inflight) return inflight;
	inflight = doRefresh().finally(() => {
		inflight = null;
	});
	return inflight;
}

async function doRefresh(): Promise<void> {
	const stage1 = await fetch(REFRESH_URL, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		credentials: 'include',
		body: '{}',
	});
	if (!stage1.ok) {
		throw new Error(`admin refresh stage1: HTTP ${stage1.status}`);
	}
	const { nonce } = (await stage1.json()) as { nonce?: string };
	if (!nonce) throw new Error('admin refresh stage1: missing nonce');

	const key = await getDeviceKey();
	const nonceBytes = base64UrlToBytes(nonce);
	const sig = await key.sign(nonceBytes);

	const stage2 = await fetch(REFRESH_URL, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		credentials: 'include',
		body: JSON.stringify({
			nonce,
			signature: bytesToBase64Url(sig),
		}),
	});
	if (!stage2.ok) {
		throw new Error(`admin refresh stage2: HTTP ${stage2.status}`);
	}
}
