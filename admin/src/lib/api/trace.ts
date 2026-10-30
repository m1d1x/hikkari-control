import { get } from './client';

export interface TraceSpan {
	traceId: string;
	spanId: string;
	parentSpanId: string;
	name: string;
	service: string;
	startedAt: string;
	durationMs: number;
	status: 'ok' | 'error';
	userId: string;
	attrs?: Record<string, unknown> | string;
}

export interface TraceResponse {
	spans: TraceSpan[];
}

export function fetchTrace(traceId: string): Promise<TraceResponse> {
	return get<TraceResponse>(`/api/v1/admin/trace/${encodeURIComponent(traceId)}`);
}

// fetchSpansBatch returns spans for a list of trace_ids in one round-trip.
// Used by the NDJSON exporter so it can interleave logs and spans without
// N round-trips. Server-side cap: 500 trace_ids per call.
export function fetchSpansBatch(traceIds: string[]): Promise<TraceResponse> {
	if (traceIds.length === 0) return Promise.resolve({ spans: [] });
	const q = encodeURIComponent(traceIds.join(','));
	return get<TraceResponse>(`/api/v1/admin/spans?trace_ids=${q}`);
}
