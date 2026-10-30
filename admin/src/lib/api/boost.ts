import { get, post } from './client';

export interface BoostStatus {
	active: boolean;
	expiresAt?: string;
	remainingS?: number;
}

export async function fetchBoostStatus(userId: string): Promise<BoostStatus> {
	return get<BoostStatus>(`/api/v1/admin/users/${encodeURIComponent(userId)}/boost-logs`);
}

export async function setBoost(userId: string, durationMinutes: number): Promise<BoostStatus> {
	return post<BoostStatus>(
		`/api/v1/admin/users/${encodeURIComponent(userId)}/boost-logs`,
		{ durationMinutes },
	);
}

export async function clearBoost(userId: string): Promise<BoostStatus> {
	return setBoost(userId, 0);
}
