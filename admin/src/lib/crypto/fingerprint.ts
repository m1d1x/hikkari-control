// Device fingerprint hash for the admin panel. Mirrors the main app's
// implementation byte-for-byte so the server-side validator (which is
// the same code path) accepts the result. Soft signal only — the
// Ed25519 key is the real device anchor.

const FINGERPRINT_VERSION = 1;

interface FingerprintFields {
	version: number;
	platform: string;
	userAgent: string;
	language: string;
	languages: string;
	timezone: string;
	screen: string;
	colorDepth: number;
	hardwareConcurrency: number;
	deviceMemory: number | null;
	touch: boolean;
}

function readFields(): FingerprintFields {
	const nav = navigator;
	const scr = screen;
	let timezone = 'unknown';
	try {
		timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'unknown';
	} catch {
		// non-fatal
	}
	return {
		version: FINGERPRINT_VERSION,
		platform: nav.platform || '',
		userAgent: nav.userAgent || '',
		language: nav.language || '',
		languages: (nav.languages || []).join(','),
		timezone,
		screen: `${scr.width}x${scr.height}`,
		colorDepth: scr.colorDepth || 0,
		hardwareConcurrency: nav.hardwareConcurrency || 0,
		deviceMemory: typeof (nav as any).deviceMemory === 'number' ? (nav as any).deviceMemory : null,
		touch: 'ontouchstart' in window || (nav.maxTouchPoints || 0) > 0,
	};
}

function canonicalize(fields: FingerprintFields): string {
	const keys = Object.keys(fields).sort();
	const parts: string[] = [];
	for (const k of keys) {
		parts.push(`${k}=${(fields as any)[k]}`);
	}
	return parts.join('|');
}

export async function fingerprintHash(): Promise<Uint8Array> {
	const fields = readFields();
	const canonical = canonicalize(fields);
	const bytes = new TextEncoder().encode(canonical);
	const hash = await crypto.subtle.digest('SHA-256', bytes);
	return new Uint8Array(hash);
}

export async function fingerprintHashBase64(): Promise<string> {
	const buf = await fingerprintHash();
	let s = '';
	for (let i = 0; i < buf.length; i++) s += String.fromCharCode(buf[i]);
	return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
