// Per-device P-256 keypair for the admin panel's session binding.
//
// The session this key pins is a proof-of-possession session: login registers
// the public key, and every refresh is a challenge the server issues and this
// key signs. A stolen refresh cookie on its own is therefore worth nothing.
//
// # Why P-256, and why these exact encodings
//
// The curve and the byte shapes are not ours to choose — they are what a
// secure element emits, because the client key is on its way into hardware
// (Secure Enclave / TPM / StrongBox all store secp256r1 and nothing else).
// The server side is `internal/auth/device_key.go`, and it accepts:
//
//   - public key: SEC1 uncompressed point, 65 bytes (0x04‖X‖Y). NOT SPKI-DER.
//   - signature:  ASN.1 DER (X9.62), over SHA-256 of the RAW nonce.
//
// This file used to generate an Ed25519 key, from before the curve change
// (migration 129). The server rejects a 32-byte key outright, so the panel
// could not log in at all — it failed at the boundary with "public_key must be
// 65 bytes" rather than verifying against anything.
//
// # What lives where
//
// The private key is a NON-EXTRACTABLE CryptoKey. IndexedDB stores CryptoKey
// objects natively, so it never exists as bytes anywhere in this process: the
// panel can sign with it and cannot read it, which is the closest a browser
// gets to the hardware model above. IndexedDB is origin-scoped, so this key is
// the admin origin's alone and unrelated to any key the app holds.

const DB_NAME = 'nekko-admin-auth';
const STORE = 'device-key';

// Keyed by curve. The old 'ed25519' entry is deliberately left alone rather
// than migrated: there is nothing to migrate — the server never accepted those
// keys — and a stale entry under its own name cannot be mistaken for this one.
const KEY = 'p256';

export interface DeviceKey {
	/** SEC1 uncompressed point, 65 bytes. */
	publicKey: Uint8Array;
	platform: string;
	isTrusted: boolean;
	/** Signs the RAW message; returns an ASN.1 DER signature. */
	sign(message: Uint8Array): Promise<Uint8Array>;
}

let cached: Promise<DeviceKey> | null = null;

export function getDeviceKey(): Promise<DeviceKey> {
	if (cached) return cached;
	cached = loadOrCreateKey();
	return cached;
}

async function loadOrCreateKey(): Promise<DeviceKey> {
	let stored = await readFromIndexedDB();
	if (!stored) {
		const pair = (await crypto.subtle.generateKey(
			{ name: 'ECDSA', namedCurve: 'P-256' },
			// Non-extractable: the private half can be used and never read.
			false,
			['sign', 'verify'],
		)) as CryptoKeyPair;
		const pub = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey));
		stored = { priv: pair.privateKey, pub };
		await writeToIndexedDB(stored);
	}
	const priv = stored.priv;
	return {
		publicKey: stored.pub,
		platform: detectWebPlatform(),
		isTrusted: false,
		sign: async (msg) => {
			// WebCrypto returns IEEE P1363 (r‖s, 64 bytes); the server reads
			// X9.62 DER, because that is what a secure element produces.
			const raw = new Uint8Array(
				await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, priv, msg as BufferSource),
			);
			return p1363ToDER(raw);
		},
	};
}

/**
 * Convert a P1363 (r‖s) ECDSA signature to ASN.1 DER.
 *
 * DER integers are signed and minimally encoded, so each half is stripped of
 * leading zero bytes and then given one back when its top bit is set —
 * otherwise a value like 0x80… reads as negative and the signature is rejected
 * by a verifier that is behaving perfectly correctly.
 */
function p1363ToDER(sig: Uint8Array): Uint8Array {
	const half = sig.length / 2;
	const r = derInteger(sig.subarray(0, half));
	const s = derInteger(sig.subarray(half));
	const body = new Uint8Array(r.length + s.length);
	body.set(r, 0);
	body.set(s, r.length);
	const out = new Uint8Array(2 + body.length);
	out[0] = 0x30; // SEQUENCE
	out[1] = body.length; // always < 128 for P-256
	out.set(body, 2);
	return out;
}

function derInteger(v: Uint8Array): Uint8Array {
	let i = 0;
	while (i < v.length - 1 && v[i] === 0) i++;
	let val = v.subarray(i);
	if (val[0] & 0x80) {
		const padded = new Uint8Array(val.length + 1);
		padded.set(val, 1);
		val = padded;
	}
	const out = new Uint8Array(2 + val.length);
	out[0] = 0x02; // INTEGER
	out[1] = val.length;
	out.set(val, 2);
	return out;
}

interface StoredKey {
	priv: CryptoKey;
	pub: Uint8Array;
}

function openDB(): Promise<IDBDatabase> {
	return new Promise((resolve, reject) => {
		const req = indexedDB.open(DB_NAME, 1);
		req.onupgradeneeded = () => {
			const db = req.result;
			if (!db.objectStoreNames.contains(STORE)) {
				db.createObjectStore(STORE);
			}
		};
		req.onsuccess = () => resolve(req.result);
		req.onerror = () => reject(req.error);
	});
}

async function readFromIndexedDB(): Promise<StoredKey | null> {
	const db = await openDB();
	return new Promise((resolve, reject) => {
		const tx = db.transaction(STORE, 'readonly');
		const req = tx.objectStore(STORE).get(KEY);
		req.onsuccess = () => {
			const v = req.result as { priv?: CryptoKey; pub?: ArrayBuffer | Uint8Array } | undefined;
			if (!v || !v.priv || !v.pub) return resolve(null);
			const pub = v.pub instanceof Uint8Array ? v.pub : new Uint8Array(v.pub);
			// A stored key from before the curve change is the wrong length.
			// Treat it as absent so a fresh one is generated rather than sent
			// to a server that will reject it.
			if (pub.length !== 65) return resolve(null);
			resolve({ priv: v.priv, pub });
		};
		req.onerror = () => reject(req.error);
	});
}

async function writeToIndexedDB(k: StoredKey): Promise<void> {
	const db = await openDB();
	await new Promise<void>((resolve, reject) => {
		const tx = db.transaction(STORE, 'readwrite');
		tx.objectStore(STORE).put({ priv: k.priv, pub: k.pub }, KEY);
		tx.oncomplete = () => resolve();
		tx.onerror = () => reject(tx.error);
	});
}

// Web platform label. The login handler stores it as-is on the session row.
function detectWebPlatform(): string {
	const ua = navigator.userAgent.toLowerCase();
	let browser = 'unknown';
	if (ua.includes('firefox')) browser = 'firefox';
	else if (ua.includes('edg/')) browser = 'edge';
	else if (ua.includes('chrome')) browser = 'chrome';
	else if (ua.includes('safari')) browser = 'safari';
	return `admin-web-${browser}`;
}

export function bytesToBase64Url(buf: Uint8Array): string {
	let s = '';
	for (let i = 0; i < buf.length; i++) s += String.fromCharCode(buf[i]);
	return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function base64UrlToBytes(s: string): Uint8Array {
	const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4);
	const bin = atob(b64);
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out;
}
