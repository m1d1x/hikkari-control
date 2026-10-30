// Client for /api/v1/admin/system-logs/* — the "System Logs" admin tab.
// Separate from lib/api/logs.ts so app-error consumers don't pull in CSP /
// probe types they never use.

import { del, get } from './client';

// ── CSP violations ─────────────────────────────────────────────────────

export interface CSPViolationEntry {
	ts: string;
	ip: string;
	documentUri: string;
	referrer: string;
	violatedDirective: string;
	effectiveDirective: string;
	blockedUri: string;
	sourceFile: string;
	lineNumber: number;
	columnNumber: number;
	disposition: string;
}

export interface CSPViolationsResponse {
	entries: CSPViolationEntry[];
	nextCursor: string | null;
}

export interface CSPViolationsFilter {
	from?: string;
	to?: string;
	directive?: string;
	blocked?: string;
	disposition?: string;
	cursor?: string;
	limit?: number;
}

export async function fetchCSPViolations(
	f: CSPViolationsFilter,
): Promise<CSPViolationsResponse> {
	const params: Record<string, string | number | undefined> = {};
	if (f.from) params.from = f.from;
	if (f.to) params.to = f.to;
	if (f.directive) params.directive = f.directive;
	if (f.blocked) params.blocked = f.blocked;
	if (f.disposition) params.disposition = f.disposition;
	if (f.cursor) params.cursor = f.cursor;
	if (f.limit) params.limit = f.limit;
	return get<CSPViolationsResponse>('/api/v1/admin/system-logs/csp', params);
}

export interface CSPViolationStat {
	directive: string;
	blockedUri: string;
	count: number;
}

export interface CSPViolationStatsResponse {
	window: string;
	total: number;
	top: CSPViolationStat[];
}

export async function fetchCSPViolationStats(
	window = '1d',
): Promise<CSPViolationStatsResponse> {
	return get<CSPViolationStatsResponse>('/api/v1/admin/system-logs/csp/stats', { window });
}

// ── Voice probes (raw rows) ────────────────────────────────────────────

export type ProbeType = 'udp' | 'tcp' | 'tls';

export interface VoiceProbeEntry {
	ts: string;
	sourceRegion: string;
	targetRegion: string;
	probeType: ProbeType;
	rttMs: number;
	jitterMs: number;
	packetLossPct: number;
	success: number;
	packetsSent: number;
	packetsRecv: number;
}

export interface VoiceProbesResponse {
	entries: VoiceProbeEntry[];
	nextCursor: string | null;
}

export interface VoiceProbesFilter {
	from?: string;
	to?: string;
	source?: string;
	target?: string;
	type?: ProbeType;
	cursor?: string;
	limit?: number;
}

export async function clearCSPViolations(): Promise<{ ok: boolean }> {
	return del<{ ok: boolean }>('/api/v1/admin/system-logs/csp');
}

export async function clearVoiceProbes(): Promise<{ ok: boolean }> {
	return del<{ ok: boolean }>('/api/v1/admin/system-logs/probes');
}

export async function fetchVoiceProbes(
	f: VoiceProbesFilter,
): Promise<VoiceProbesResponse> {
	const params: Record<string, string | number | undefined> = {};
	if (f.from) params.from = f.from;
	if (f.to) params.to = f.to;
	if (f.source) params.source = f.source;
	if (f.target) params.target = f.target;
	if (f.type) params.type = f.type;
	if (f.cursor) params.cursor = f.cursor;
	if (f.limit) params.limit = f.limit;
	return get<VoiceProbesResponse>('/api/v1/admin/system-logs/probes', params);
}
