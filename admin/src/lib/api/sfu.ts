import { get } from './client';

export interface SFURegionSnapshot {
	region: string;
	// "up" when the SFU is scraping cleanly, "down" when prom can't reach
	// it. The card stays in the grid in both states — operators must see
	// dead nodes, not have them silently vanish.
	status: 'up' | 'down';
	roomsActive: number;
	peersActive: number;
	packetsAudioPerSec: number;
	packetsScreenVideoPerSec: number;
	packetsScreenAudioPerSec: number;
	hostCpuPercent: number;
	hostMemPercent: number;
}

export interface SFUSnapshotResponse {
	regions: SFURegionSnapshot[];
	available: boolean;
}

// Live per-region voice load. Refresh ~5s on the page; the underlying
// Prom queries hit instant API and are cheap.
export async function getSFUSnapshot(): Promise<SFUSnapshotResponse> {
	return get<SFUSnapshotResponse>('/api/v1/admin/sfu/snapshot');
}

export interface SFUNodeDetail {
	region: string;
	publicUrl: string;
	roomsActive: number;
	peersActive: number;
	packetsAudioPerSec: number;
	packetsScreenVideoPerSec: number;
	packetsScreenAudioPerSec: number;
	goroutinesCount: number;
	goMemAllocBytes: number;
	processCpuSeconds: number;
	uptimeSeconds: number;
	hostCpuPercent: number;
	hostMemUsedBytes: number;
	hostMemTotalBytes: number;
	hostMemPercent: number;

	// Signaling rate-limit (L1-L4). All rates are events/sec on this shard.
	// sigEnforceMode reflects the current mode of the shard (0=observe,
	// 1=enforce). When 0, "drop" series stay flat — operator sees only the
	// "violations" series and can size thresholds before flipping enforce.
	sigEnforceMode: number;
	sigL1AllowedGeneralPerSec: number;
	sigL1AllowedICEPerSec: number;
	sigL1ViolGeneralPerSec: number;
	sigL1ViolICEPerSec: number;
	sigL1DropGeneralPerSec: number;
	sigL1DropICEPerSec: number;
	sigL2ViolPerSec: number;
	sigL2DropPerSec: number;
	sigL3AllowedPerSec: number;
	sigL3BucketHitsPerSec: number;
	sigL3BackoffHitsPerSec: number;
	sigL3DropPerSec: number;
	sigL4RoomHitsPerSec: number;
	sigL4AccountHitsPerSec: number;
	sigL4DropPerSec: number;
}

export async function getSFUNodeDetail(region: string): Promise<SFUNodeDetail> {
	return get<SFUNodeDetail>(`/api/v1/admin/sfu/nodes/${encodeURIComponent(region)}`);
}

export interface SFUNodeHistoryPoint {
	ts: number;
	value: number;
}

export interface SFUNodeHistory {
	region: string;
	range: string;
	rooms: SFUNodeHistoryPoint[];
	peers: SFUNodeHistoryPoint[];
	audio: SFUNodeHistoryPoint[];
	svideo: SFUNodeHistoryPoint[];
	saudio: SFUNodeHistoryPoint[];
	host_cpu: SFUNodeHistoryPoint[];
	host_mem: SFUNodeHistoryPoint[];

	// Mesh / chat history (snake_case mirrors the wire format).
	chat_local: SFUNodeHistoryPoint[];
	chat_mesh_sent: SFUNodeHistoryPoint[];
	chat_mesh_recv: SFUNodeHistoryPoint[];
	chat_mesh_bytes_sent: SFUNodeHistoryPoint[];
	chat_mesh_bytes_recv: SFUNodeHistoryPoint[];
	dm_local: SFUNodeHistoryPoint[];
	dm_mesh_sent: SFUNodeHistoryPoint[];
	dm_mesh_recv: SFUNodeHistoryPoint[];
	dm_mesh_bytes_sent: SFUNodeHistoryPoint[];
	dm_mesh_bytes_recv: SFUNodeHistoryPoint[];
	viewer_hits: SFUNodeHistoryPoint[];
	viewer_misses: SFUNodeHistoryPoint[];
	viewer_invalidates: SFUNodeHistoryPoint[];

	// Signaling rate-limit history. Per-layer time-series so each chart
	// (L1/L2/L3/L4) can stack violations + dropped together.
	sig_l1_viol_gen: SFUNodeHistoryPoint[];
	sig_l1_viol_ice: SFUNodeHistoryPoint[];
	sig_l1_drop_gen: SFUNodeHistoryPoint[];
	sig_l1_drop_ice: SFUNodeHistoryPoint[];
	sig_l2_viol: SFUNodeHistoryPoint[];
	sig_l2_drop: SFUNodeHistoryPoint[];
	sig_l3_bucket: SFUNodeHistoryPoint[];
	sig_l3_backoff: SFUNodeHistoryPoint[];
	sig_l3_drop: SFUNodeHistoryPoint[];
	sig_l4_room: SFUNodeHistoryPoint[];
	sig_l4_account: SFUNodeHistoryPoint[];
	sig_l4_drop: SFUNodeHistoryPoint[];
}

export async function getSFUNodeHistory(region: string, range = '1h'): Promise<SFUNodeHistory> {
	return get<SFUNodeHistory>(
		`/api/v1/admin/sfu/nodes/${encodeURIComponent(region)}/history?range=${encodeURIComponent(range)}`,
	);
}

// ── Synthetic uptime/loss probes ───────────────────────────────────────
//
// Each series is keyed by source-region — the prober that took the sample.
// We render one line per source so an operator can see "uptime as measured
// from ru-msk vs eu-nl" side by side.

export interface SFUProbePoint {
	ts: number;
	value: number;
}
export interface SFUProbeSeries {
	sourceRegion: string;
	points: SFUProbePoint[];
}
export interface SFUProbesResponse {
	region: string;
	range: string;
	bucket: string;
	uptime: SFUProbeSeries[];
	loss: SFUProbeSeries[];
	rtt: SFUProbeSeries[];
	sources: string[];
}

export async function getSFUProbes(region: string, range = '1h'): Promise<SFUProbesResponse> {
	return get<SFUProbesResponse>(
		`/api/v1/admin/sfu/probes?region=${encodeURIComponent(region)}&range=${encodeURIComponent(range)}`,
	);
}

// ── Client-side WebRTC quality aggregates ──────────────────────────────

export interface SFUClientStatPoint {
	ts: number;
	value: number;
}
export interface SFUClientStatsResponse {
	region: string;
	range: string;
	bucket: string;
	jitterP50Ms: SFUClientStatPoint[];
	jitterP95Ms: SFUClientStatPoint[];
	lossP95Pct: SFUClientStatPoint[];
	rttP50Ms: SFUClientStatPoint[];
	rttP95Ms: SFUClientStatPoint[];
	samplesPerBucket: SFUClientStatPoint[];
}

// Pass region="all" to aggregate across regions — used on the SFU mesh page
// to show the global Client quality block.
export async function getSFUClientStats(region: string, range = '1h'): Promise<SFUClientStatsResponse> {
	return get<SFUClientStatsResponse>(
		`/api/v1/admin/sfu/client-stats?region=${encodeURIComponent(region)}&range=${encodeURIComponent(range)}`,
	);
}
