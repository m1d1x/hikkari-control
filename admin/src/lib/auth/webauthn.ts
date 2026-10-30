// Passkeys in the browser for the admin panel (roadmap task 77.2).
//
// The server sends WebAuthn options as JSON with every binary field in
// base64url (go-webauthn's shape). The browser API wants ArrayBuffers in, and
// hands ArrayBuffers back. The conversion is done here by hand rather than
// through PublicKeyCredential.parse*OptionsFromJSON / toJSON(): those are
// recent enough that a slightly older Safari would turn «sign in» into a
// TypeError, and the conversion is twenty lines.

export function b64urlToBuf(s: string): ArrayBuffer {
	const pad = '='.repeat((4 - (s.length % 4)) % 4);
	const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out.buffer;
}

export function bufToB64url(buf: ArrayBuffer | ArrayBufferView): string {
	const bytes =
		buf instanceof ArrayBuffer ? new Uint8Array(buf) : new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
	let bin = '';
	for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
	return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

interface JSONDescriptor {
	id: string;
	type: string;
	transports?: string[];
}

/** Server JSON → options navigator.credentials.get accepts. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function toRequestOptions(o: any): PublicKeyCredentialRequestOptions {
	return {
		...o,
		challenge: b64urlToBuf(o.challenge),
		allowCredentials: (o.allowCredentials ?? []).map((c: JSONDescriptor) => ({
			...c,
			id: b64urlToBuf(c.id),
		})),
	};
}

/** Server JSON → options navigator.credentials.create accepts. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function toCreationOptions(o: any): PublicKeyCredentialCreationOptions {
	return {
		...o,
		challenge: b64urlToBuf(o.challenge),
		user: { ...o.user, id: b64urlToBuf(o.user.id) },
		excludeCredentials: (o.excludeCredentials ?? []).map((c: JSONDescriptor) => ({
			...c,
			id: b64urlToBuf(c.id),
		})),
	};
}

/** A login/step-up answer, serialized the way the server parses it. */
function assertionJSON(cred: PublicKeyCredential) {
	const r = cred.response as AuthenticatorAssertionResponse;
	return {
		id: cred.id,
		rawId: bufToB64url(cred.rawId),
		type: cred.type,
		response: {
			clientDataJSON: bufToB64url(r.clientDataJSON),
			authenticatorData: bufToB64url(r.authenticatorData),
			signature: bufToB64url(r.signature),
			userHandle: r.userHandle ? bufToB64url(r.userHandle) : undefined,
		},
		clientExtensionResults: cred.getClientExtensionResults(),
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
	};
}

/** A registration answer, serialized the way the server parses it. */
function attestationJSON(cred: PublicKeyCredential) {
	const r = cred.response as AuthenticatorAttestationResponse;
	return {
		id: cred.id,
		rawId: bufToB64url(cred.rawId),
		type: cred.type,
		response: {
			clientDataJSON: bufToB64url(r.clientDataJSON),
			attestationObject: bufToB64url(r.attestationObject),
			transports: typeof r.getTransports === 'function' ? r.getTransports() : undefined,
		},
		clientExtensionResults: cred.getClientExtensionResults(),
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
	};
}

/** Touch the passkey for a login or step-up challenge. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function getAssertion(options: any): Promise<unknown> {
	const cred = (await navigator.credentials.get({
		publicKey: toRequestOptions(options),
	})) as PublicKeyCredential | null;
	if (!cred) throw new Error('passkey cancelled');
	return assertionJSON(cred);
}

/** Register a new passkey for the given creation options. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function createCredential(options: any): Promise<unknown> {
	const cred = (await navigator.credentials.create({
		publicKey: toCreationOptions(options),
	})) as PublicKeyCredential | null;
	if (!cred) throw new Error('passkey cancelled');
	return attestationJSON(cred);
}

/** True when the person dismissed the system prompt rather than failing it. */
export function isCancelled(e: unknown): boolean {
	return e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError');
}

/** A name for a new passkey that tells the operator which device holds it. */
export function defaultPasskeyName(): string {
	const ua = navigator.userAgent;
	if (/Macintosh/.test(ua)) return 'Mac';
	if (/iPhone/.test(ua)) return 'iPhone';
	if (/iPad/.test(ua)) return 'iPad';
	if (/Windows/.test(ua)) return 'Windows';
	if (/Android/.test(ua)) return 'Android';
	return 'passkey';
}
