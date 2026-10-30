import { get, put, del } from './client';

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error';
export type LogSource = 'frontend' | 'backend';

export interface AdminLogEntry {
	ts: string;
	level: LogLevel;
	source: LogSource;
	userId: string;
	sessionId: string;
	cat: string;
	msg: string;
	ctx: unknown;
	serverId: string;
	traceId?: string;
	requestId?: string;
}

export interface AdminLogsResponse {
	entries: AdminLogEntry[];
	nextCursor: string | null;
}

export interface AdminLogsFilter {
	level?: string[];
	source?: string;
	cat?: string[];
	userId?: string;
	q?: string;
	from?: string;
	to?: string;
	cursor?: string;
	limit?: number;
}

export interface AdminLogCats {
	levels: string[];
	sources: string[];
	cats: string[];
}

export async function fetchAdminLogs(
	f: AdminLogsFilter,
): Promise<AdminLogsResponse> {
	const params: Record<string, string | number | undefined> = {};
	if (f.level?.length) params.level = f.level.join(',');
	if (f.source) params.source = f.source;
	if (f.cat?.length) params.cat = f.cat.join(',');
	if (f.userId) params.user_id = f.userId;
	if (f.q) params.q = f.q;
	if (f.from) params.from = f.from;
	if (f.to) params.to = f.to;
	if (f.cursor) params.cursor = f.cursor;
	if (f.limit) params.limit = f.limit;
	return get<AdminLogsResponse>('/api/v1/admin/logs', params);
}

export async function fetchAdminLogCats(): Promise<AdminLogCats> {
	return get<AdminLogCats>('/api/v1/admin/logs/cats');
}

export async function clearAdminLogs(): Promise<{ ok: boolean }> {
	return del<{ ok: boolean }>('/api/v1/admin/logs');
}

// ── Log config (Phase 5) ───────────────────────────────────────

export type LogConfigScope = 'global' | 'user';

export interface LogConfigEntry {
	id: number;
	scope: LogConfigScope;
	userId?: string | null;
	category: string;
	minLevel: LogLevel;
	remoteEnabled: boolean;
	expiresAt?: string | null;
	updatedAt: string;
}

export interface LogConfigListResponse {
	entries: LogConfigEntry[];
}

export interface LogConfigUpsert {
	scope: LogConfigScope;
	userId?: string;
	category: string;
	minLevel: LogLevel;
	remoteEnabled: boolean;
	expiresAt?: string | null;
}

export async function fetchLogConfig(): Promise<LogConfigListResponse> {
	return get<LogConfigListResponse>('/api/v1/admin/log-config');
}

export async function fetchUserLogConfig(userId: string): Promise<LogConfigListResponse> {
	return get<LogConfigListResponse>(`/api/v1/admin/log-config/user/${encodeURIComponent(userId)}`);
}

export async function upsertLogConfig(entry: LogConfigUpsert): Promise<{ ok: boolean }> {
	return put<{ ok: boolean }>('/api/v1/admin/log-config', entry);
}

export async function deleteLogConfig(id: number): Promise<{ ok: boolean }> {
	return del<{ ok: boolean }>(`/api/v1/admin/log-config/${id}`);
}

// ── WebSocket ticket ───────────────────────────────────────────────────────

/** Fetches a short-lived JWT (60s) for authenticating WS connections. */
export async function fetchWSTicket(): Promise<string> {
	const res = await get<{ ticket: string }>('/api/v1/admin/ws-ticket');
	return res.ticket;
}
