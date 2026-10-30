// The operator's own factors and the panel's destructive-action guard
// (roadmap task 77). Every call that changes a factor needs a fresh passkey
// touch; client.ts takes it automatically when the server asks.

import { get, post, del } from './client';
import { createCredential, defaultPasskeyName } from '$lib/auth/webauthn';

export interface Passkey {
	id: string;
	name: string;
	createdAt: string;
	lastUsedAt?: string;
}

export interface SecurityState {
	login: string;
	role: 'owner' | 'support';
	passkeys: Passkey[];
	recoveryCodesLeft: number;
	steppedUpUntil?: string;
}

export const fetchSecurity = () => get<SecurityState>('/api/v1/admin/security');

/** Registers another passkey on this device (Touch ID prompt). */
export async function addPasskey(name = defaultPasskeyName()): Promise<SecurityState> {
	const begin = await post<{ ceremonyId: string; options: unknown }>('/api/v1/admin/security/passkeys/begin');
	const credential = await createCredential(begin.options);
	return post<SecurityState>('/api/v1/admin/security/passkeys/finish', {
		ceremonyId: begin.ceremonyId,
		credential,
		name,
	});
}

export const removePasskey = (id: string) => del<SecurityState>(`/api/v1/admin/security/passkeys/${id}`);

/** Replaces every recovery code with ten new ones and returns them once. */
export const regenerateRecoveryCodes = () => post<{ recoveryCodes: string[] }>('/api/v1/admin/security/recovery-codes');

/** Shortest password the server accepts (auth.AdminPasswordMinLen). */
export const PASSWORD_MIN_LEN = 12;

/**
 * Changes the operator's own password. Every other signed-in browser is
 * signed out; this one stays.
 */
export const changePassword = (newPassword: string) =>
	post<{ sessionsRevoked: number }>('/api/v1/admin/security/password', { newPassword });

export interface AuditAlert {
	id: number;
	ts: string;
	admin: string;
	action: string;
	targetType: string;
	targetId: string;
}

export const fetchAuditAlerts = () => get<{ alerts: AuditAlert[] }>('/api/v1/admin/audit/alerts');
export const markAuditAlertsSeen = () => post<{ ok: boolean }>('/api/v1/admin/audit/alerts/seen');

export interface ScheduledErasure {
	userId: string;
	username: string;
	displayName: string;
	scheduledAt: string;
	eraseAt: string;
	by: string;
}

export const fetchScheduledErasures = () => get<{ erasures: ScheduledErasure[] }>('/api/v1/admin/users/erasures');
export const cancelErasure = (userId: string) =>
	post<{ id: string; cancelled: boolean }>(`/api/v1/admin/users/${userId}/erasure/cancel`);

/**
 * A peer's device list that a client refused as forged (task 76.2′): one per
 * (observer, peer, reason). The person was shown nothing; this is the only
 * place it surfaces. Owners only.
 */
export interface E2EAlert {
	id: number;
	observerId: string;
	observerName: string;
	peerId: string;
	peerName: string;
	reason: 'identity_mismatch' | 'rollback' | 'invalid' | 'list_withheld' | string;
	severity: 'warning' | 'critical';
	pinned: string;
	heldVersion: number;
	detail: string;
	firstSeen: string;
	lastSeen: string;
	occurrences: number;
	acknowledgedAt?: string;
}

export const fetchE2EAlerts = () => get<{ alerts: E2EAlert[] }>('/api/v1/admin/security/e2e-alerts');
export const ackE2EAlert = (id: number) => post<{ ok: boolean }>(`/api/v1/admin/security/e2e-alerts/${id}/ack`);

/** What each reason means, in the panel's words. */
export const E2E_REASON_TEXT: Record<string, string> = {
	identity_mismatch: 'список подписан чужим ключом',
	rollback: 'сервер откатил список устройств',
	invalid: 'список не прошёл проверку',
	list_withheld: 'сервер не отдал список',
};
