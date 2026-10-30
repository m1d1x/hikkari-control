import type { MetricValue, TimeSeries } from './common';

export interface OverviewMetrics {
	dau: MetricValue;
	wau: MetricValue;
	mau: MetricValue;
	stickiness: MetricValue; // DAU/MAU ratio
	newUsers: MetricValue;
	totalEvents: MetricValue;
	avgSessionsPerUser: MetricValue;
	discoverRate: number; // % of DAU that opened Discover
	dauTimeSeries: TimeSeries;
	wauTimeSeries: TimeSeries;
	mauTimeSeries: TimeSeries;
	newUsersTimeSeries: TimeSeries;
	discoverRateTimeSeries: TimeSeries;
	stickinessTimeSeries: TimeSeries;
	eventsByType: { type: string; count: number }[];
	settingsByTab: { type: string; count: number }[];
}

export interface AnalyticsEvent {
	id: string;
	eventType: string;
	userId: string;
	username: string;
	sessionId: string;
	properties: Record<string, unknown>;
	createdAt: string;
}

export interface EventTypeOption {
	value: string;
	label: string;
	count: number;
}

export interface FunnelStep {
	name: string;
	eventType: string;
	count: number;
	conversionRate: number;
	dropoffRate: number;
	medianTime?: number; // ms between this and previous step
}

export interface FunnelConfig {
	name: string;
	steps: { eventType: string; label: string }[];
}

export interface SignalDetail {
	value: unknown;
	points: number;
}

export interface ScoringLogEntry {
	id: number;
	scoredAt: string;
	totalScore: number;
	action: string;
	breakdown: Record<string, SignalDetail>;
	ipAddress: string | null;
	asnOrg: string | null;
	fpHash: string | null;
	powTimeMs: number | null;
}

export interface UserProfile {
	id: string;
	username: string;
	createdAt: string;
	lastSeen: string;
	totalMessages: number;
	totalSessions: number;
	serverCount: number;
	servers: UserServer[];
	sessions: UserSessionInfo[];
	recentEvents: AnalyticsEvent[];
	riskScore: number | null;
	needPhoneVerification: boolean;
	isShadowbanned: boolean;
	scoringLog: ScoringLogEntry[];
}

export interface UserSessionInfo {
	id: string;
	platform: string;
	isTrusted: boolean;
	lastIp: string | null;
	lastUserAgent: string | null;
	fingerprintHash: string;
	publicKey: string;
	createdAt: string;
	lastUsedAt: string;
	expiresAt: string;
	revokedAt: string | null;
	revokedReason: string | null;
	isActive: boolean;
}

export interface UserServer {
	id: string;
	name: string;
	joinedAt: string;
	messageCount: number;
	engagementWeight: number;
}

export interface UserListItem {
	id: string;
	username: string;
	createdAt: string;
	lastSeen: string | null;
	totalMessages: number;
	serverCount: number;
	eventsToday: number;
	avgSessionMs: number;
	maxSessionMs: number;
	stickiness: number; // % of days visited in last 30 days (0-100)
	s3StorageBytes: number;
	fraudScore: number; // 0-100
	isShadowbanned: boolean;
	isBanned: boolean;
	/** Set while an erasure ordered from the panel is pending (task 77.3). */
	eraseAt?: string;
}

export interface ServerAnalytics {
	id: string;
	name: string;
	description: string;
	memberCount: number;
	onlineCount: number;
	createdAt: string;
	messagesPerDay: number;
	dauServer: number;
	retention1d: number;
	retention7d: number;
	retention30d: number;
	growthTimeSeries: TimeSeries;
	discoverImpressions: number;
	discoverCTR: number;
	discoverJoinRate: number;
	avgDwellTime: number;
	tsAlpha: number;
	tsBeta: number;
}

export interface ServerListItem {
	id: string;
	name: string;
	memberCount: number;
	onlineCount: number;
	messagesPerDay: number;
	growthPercent: number;
	createdAt: string;
}

export interface OnlinePoint {
	ts: string;
	value: number;
}

export interface UxFrustrationPoint {
	ts: string;
	rage: number;
	dead: number;
	quick: number;
}

export interface RealtimeStats {
	/** Accounts with the app open — a live gateway socket. "Who is here". */
	onlineUsers: number;
	/** Accounts attached to a voice shard. SFU load, not app population. */
	inVoiceUsers: number;
	eventsPerSecond: number;
	activeDiscoverSessions: number;
	recentJoins: RealtimeJoin[];
	eventRateHistory: number[];
	onlineHistory: OnlinePoint[];
	voiceHistory: OnlinePoint[];
	eventsPerSecondHistory: OnlinePoint[];
	uxFrustrationHistory?: UxFrustrationPoint[];
	uxCurrent?: { rage: number; dead: number; quick: number };
}

export interface RealtimeJoin {
	id: string;
	username: string;
	serverName: string;
	source: 'discover' | 'invite' | 'search';
	timestamp: string;
}

export interface RetentionCohort {
	cohortWeek: string;
	size: number;
	weeks: number[]; // retention % for week 0, 1, 2, ...
}

export interface PeakHoursData {
	day: number; // 0-6 (Mon-Sun)
	hour: number; // 0-23
	value: number;
}

// ===== UX Health =====

export interface UXHealthData {
	rageClicks: { selector: string; count: number }[];
	deadClicks: { selector: string; count: number }[];
	quickBacks: { from: string; to: string; count: number; avgMs: number }[];
	timeSeries: { date: string; rage: number; dead: number; quick: number }[];
	totalRage: number;
	totalDead: number;
	totalQuick: number;
}

// ===== Client Performance =====

export interface ClientPerfLog {
	userId: string;
	username: string;
	sessionId: string;
	eventType: string;
	properties: string;
	createdAt: string;
}

export interface ClientPerfData {
	slowEndpoints: { endpoint: string; count: number; avgMs: number }[];
	clientErrors: { message: string; count: number }[];
	timeSeries: { date: string; slowApis: number; jsErrors: number }[];
	totalSlow: number;
	totalErrors: number;
	logs: ClientPerfLog[];
}

// ===== Sessions =====

export interface SessionsData {
	avgDurationMs: number;
	totalSessions: number;
	distribution: { label: string; count: number }[];
}

// ===== Onboarding =====

export interface OnboardingData {
	steps: { name: string; count: number; rate: number }[];
}

// ===== Traffic =====

export interface SessionLog {
	sessionId: string;
	userId: string;
	username: string;
	startedAt: string;
	countryCode: string;
	countryName: string;
	city: string;
	browser: string;
	os: string;
	deviceType: string;
	source: string;
}

export interface TrafficData {
	totalSessions: number;
	uniqueUsers: number;
	referralSources: ReferralSource[];
	geoTop: GeoEntry[];
	regionTopRu: GeoEntry[];
	devices: TrafficSplit[];
	osSplit: TrafficSplit[];
	browserSplit: TrafficSplit[];
	sessionLogs: SessionLog[];
}

export interface ReferralSource {
	source: string;
	label: string;
	sessions: number;
	pct: number;
}

export interface GeoEntry {
	countryCode?: string;
	countryName?: string;
	region?: string;
	sessions: number;
	pct: number;
}

export interface TrafficSplit {
	label: string;
	sessions: number;
	pct: number;
}
