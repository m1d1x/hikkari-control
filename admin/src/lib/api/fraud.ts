import { get, post, put, del } from './client';

export interface DatacenterPattern {
	id: number;
	pattern: string;
	provider: string;
	enabled: boolean;
	createdAt: string;
}

export async function getPatterns(): Promise<DatacenterPattern[]> {
	return get<DatacenterPattern[]>('/api/v1/admin/fraud/patterns');
}

export async function createPattern(pattern: string, provider: string): Promise<{ id: number }> {
	return post<{ id: number }>('/api/v1/admin/fraud/patterns', { pattern, provider });
}

export async function togglePattern(id: number): Promise<void> {
	await put('/api/v1/admin/fraud/patterns/' + id);
}

export async function deletePattern(id: number): Promise<void> {
	await del('/api/v1/admin/fraud/patterns/' + id);
}

export async function reloadCache(): Promise<void> {
	await post('/api/v1/admin/fraud/reload');
}

// ── Scoring config ────────────────────────────────────

export interface ScoringSignal {
	key: string;
	label: string;
	category: string;
	points: number;
	enabled: boolean;
}

export interface ActionThresholds {
	passMax: number;
	smsVerifyMax: number;
	shadowbanMax: number;
}

export interface ScoringConfigResponse {
	signals: ScoringSignal[];
	thresholds: ActionThresholds;
}

export async function getScoringConfig(): Promise<ScoringConfigResponse> {
	return get<ScoringConfigResponse>('/api/v1/admin/fraud/scoring');
}

export async function updateSignal(key: string, points: number, enabled: boolean): Promise<void> {
	await put('/api/v1/admin/fraud/scoring/signals/' + key, { points, enabled });
}

export async function updateThresholds(thresholds: ActionThresholds): Promise<void> {
	await put('/api/v1/admin/fraud/scoring/thresholds', thresholds);
}

export async function reloadScoringCache(): Promise<void> {
	await post('/api/v1/admin/fraud/scoring/reload');
}

// ── The screen's measured half (plan 42 К7) ────────────────────────────────
//
// Both charts on this page used to be generated: the daily outcomes were a
// sine with a "botnet" spike at 75 % of the window, and the histogram was two
// Gaussians drawn inside the component. Both are read from
// `fraud_scoring_log` now — one row per verdict as it was passed.

/** One figure the hub cannot compute, with the reason. */
export interface FraudMissingFigure {
	figure: string;
	why: string;
}

/** One bucket of the outcome series. */
export interface FraudHistoryBucket {
	ts: number;
	pass: number;
	smsVerify: number;
	shadowban: number;
	silentBlock: number;
}

/** One 5-point slice of the score range. `to` is inclusive. */
export interface FraudScoreBar {
	from: number;
	to: number;
	count: number;
}

export interface FraudOutcomeCount {
	pass: number;
	smsVerify: number;
	shadowban: number;
	silentBlock: number;
	total: number;
}

export interface FraudHistory {
	range: '24h' | '7d' | '30d';
	buckets: FraudHistoryBucket[];
	bucketSeconds: number;
	histogram: FraudScoreBar[];
	thresholds: ActionThresholds;
	totals: FraudOutcomeCount;
	/** scorings in the window; 0 means the charts have nothing to show */
	scored: number;
	missing: FraudMissingFigure[];
}

export async function getFraudHistory(range: '24h' | '7d' | '30d' = '7d'): Promise<FraudHistory> {
	return get<FraudHistory>('/api/v1/admin/fraud/history?range=' + encodeURIComponent(range));
}
