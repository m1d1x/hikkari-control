import { fetchTrace, type TraceResponse } from './trace';

const cache = new Map<string, TraceResponse>();
const inflight = new Map<string, Promise<TraceResponse>>();

export function getCachedTrace(traceId: string): Promise<TraceResponse> {
	const hit = cache.get(traceId);
	if (hit) return Promise.resolve(hit);
	const pending = inflight.get(traceId);
	if (pending) return pending;
	const p = fetchTrace(traceId)
		.then((res) => {
			cache.set(traceId, res);
			inflight.delete(traceId);
			return res;
		})
		.catch((err) => {
			inflight.delete(traceId);
			throw err;
		});
	inflight.set(traceId, p);
	return p;
}

export function peekCachedTrace(traceId: string): TraceResponse | undefined {
	return cache.get(traceId);
}
