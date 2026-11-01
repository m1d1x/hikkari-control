import type {
	OverviewMetrics,
	AnalyticsEvent,
	EventTypeOption,
	FunnelStep,
	UserListItem,
	UserProfile,
	ServerListItem,
	ServerAnalytics,
	RealtimeStats,
	RetentionCohort,
	PeakHoursData,
	TrafficData,
	UXHealthData,
} from '$lib/types/analytics';
import {
	generateTimeSeries,
	generateSparkData,
	generateUUID,
	generateUsername,
	generateServerName,
	randomInt,
	randomFloat,
	randomItem,
	daysAgo,
	hoursAgo,
	minutesAgo,
} from './generators';
import { withDelay } from './delay';

export function getOverviewMetrics(): Promise<OverviewMetrics> {
	const dauSeries = generateTimeSeries(30, 1250, 200, 0.15);
	const wauSeries = generateTimeSeries(8, 3800, 400, 0.1);
	const mauSeries = generateTimeSeries(6, 8900, 800, 0.08);
	const newUsersSeries = generateTimeSeries(30, 340, 60, 0.1);
	const discoverRateSeries = generateTimeSeries(30, 28, 5, 0.05);
	const stickinessSeries = generateTimeSeries(30, 14, 2, 0.02);
	return withDelay({
		dau: { value: 1247, previousValue: 1183 },
		wau: { value: 3842, previousValue: 3654 },
		mau: { value: 8930, previousValue: 8210 },
		stickiness: { value: 14.0, previousValue: 14.4 },
		newUsers: { value: 342, previousValue: 298 },
		totalEvents: { value: 284500, previousValue: 251200 },
		avgSessionsPerUser: { value: 3.2, previousValue: 2.9 },
		discoverRate: 28.4,
		dauTimeSeries: { label: 'DAU', data: dauSeries },
		wauTimeSeries: { label: 'WAU', data: wauSeries },
		mauTimeSeries: { label: 'MAU', data: mauSeries },
		newUsersTimeSeries: { label: 'New Users', data: newUsersSeries },
		discoverRateTimeSeries: { label: 'Discover Rate', data: discoverRateSeries },
		stickinessTimeSeries: { label: 'Stickiness', data: stickinessSeries },
		eventsByType: [
			{ type: 'message_sent', count: 124500 },
			{ type: 'page_view', count: 68200 },
			{ type: 'app_open', count: 42100 },
			{ type: 'card_impression', count: 28400 },
			{ type: 'server_join', count: 8900 },
			{ type: 'card_preview', count: 7200 },
			{ type: 'discover_open', count: 3800 },
			{ type: 'server_leave', count: 1400 },
		],
		settingsByTab: [
			{ type: 'settings:profile', count: 4200 },
			{ type: 'settings:audio', count: 3100 },
			{ type: 'settings:notifications', count: 1850 },
			{ type: 'settings:privacy', count: 1240 },
			{ type: 'settings:appearance', count: 960 },
		],
	});
}

const eventTypes = [
	'app_open',
	'page_view',
	'message_sent',
	'server_join',
	'server_leave',
	'discover_open',
	'card_impression',
	'card_preview',
	'discover_scroll_speed',
];

export function getEventTypes(): Promise<EventTypeOption[]> {
	return withDelay(
		eventTypes.map((t) => ({
			value: t,
			label: t,
			count: randomInt(1000, 50000),
		})),
	);
}

export function getEvents(
	page = 1,
	pageSize = 20,
	eventType?: string,
): Promise<{ data: AnalyticsEvent[]; total: number }> {
	const events: AnalyticsEvent[] = Array.from({ length: pageSize }, (_, i) => {
		const type = eventType || randomItem(eventTypes);
		return {
			id: generateUUID(),
			eventType: type,
			userId: generateUUID(),
			username: generateUsername(),
			sessionId: generateUUID(),
			properties: generateEventProperties(type),
			createdAt: minutesAgo(randomInt(1, 1440)),
		};
	});
	return withDelay({ data: events, total: 15420 });
}

function generateEventProperties(type: string): Record<string, unknown> {
	switch (type) {
		case 'card_impression':
			return {
				server_id: generateUUID(),
				position_in_feed: randomInt(1, 50),
				card_size: randomItem(['large', 'medium', 'small']),
				viewport_dwell_ms: randomInt(200, 8000),
				strategy_mode: randomItem(['exploitation', 'exploration']),
			};
		case 'server_join':
			return {
				server_id: generateUUID(),
				source: randomItem(['discover', 'invite', 'search']),
			};
		case 'page_view':
			return {
				page: randomItem(['discover', 'chat', 'settings', 'voice']),
			};
		default:
			return {};
	}
}

export function getFunnelSteps(steps: string[]): Promise<FunnelStep[]> {
	let count = randomInt(5000, 12000);
	return withDelay(
		steps.map((name, i) => {
			const prev = count;
			if (i > 0) count = Math.round(count * randomFloat(0.25, 0.65));
			return {
				name,
				eventType: name,
				count,
				conversionRate: i === 0 ? 100 : (count / prev) * 100,
				dropoffRate: i === 0 ? 0 : 100 - (count / prev) * 100,
				medianTime: i === 0 ? undefined : randomInt(2000, 120000),
			};
		}),
	);
}

// A fixed pool, generated once. Regenerating the rows on every call made
// sorting and pagination unobservable in mock mode: each page was a fresh set
// of random people.
const userPool: UserListItem[] = Array.from({ length: 137 }, () => ({
	id: generateUUID(),
	username: generateUsername(),
	createdAt: daysAgo(randomInt(1, 365)),
	lastSeen: hoursAgo(randomInt(0, 48)),
	totalMessages: randomInt(10, 15000),
	serverCount: randomInt(1, 20),
	eventsToday: randomInt(5, 500),
	avgSessionMs: randomInt(2 * 60_000, 45 * 60_000),
	maxSessionMs: randomInt(30 * 60_000, 8 * 3_600_000),
	stickiness: randomInt(5, 95),
	s3StorageBytes: randomInt(100 * 1024, 500 * 1024 * 1024),
	fraudScore: randomInt(0, 100),
	isShadowbanned: Math.random() < 0.1,
	isBanned: Math.random() < 0.05,
}));

export function getUsers(
	page = 1,
	pageSize = 20,
	query: { q?: string; id?: string; sort?: string; dir?: 'asc' | 'desc' } = {},
): Promise<{ data: UserListItem[]; total: number }> {
	const q = (query.q ?? '').toLowerCase();
	const idPrefix = (query.id ?? '').toLowerCase();

	let rows = userPool.filter(
		(u) => (!q || u.username.toLowerCase().includes(q)) && (!idPrefix || u.id.toLowerCase().startsWith(idPrefix)),
	);

	const key = query.sort || 'createdAt';
	const sign = query.dir === 'asc' ? 1 : -1;
	rows = [...rows].sort((a, b) => {
		const av = (a as unknown as Record<string, unknown>)[key];
		const bv = (b as unknown as Record<string, unknown>)[key];
		if (typeof av === 'number' && typeof bv === 'number') return sign * (av - bv);
		return sign * String(av ?? '').localeCompare(String(bv ?? ''), 'ru', { numeric: true });
	});

	const offset = (page - 1) * pageSize;
	return withDelay({ data: rows.slice(offset, offset + pageSize), total: rows.length });
}

export function getUserProfile(userId: string): Promise<UserProfile> {
	return withDelay({
		id: userId,
		username: generateUsername(),
		createdAt: daysAgo(randomInt(30, 365)),
		lastSeen: hoursAgo(randomInt(0, 4)),
		totalMessages: randomInt(500, 25000),
		totalSessions: randomInt(50, 500),
		serverCount: randomInt(3, 15),
		servers: Array.from({ length: randomInt(3, 10) }, () => ({
			id: generateUUID(),
			name: generateServerName(),
			joinedAt: daysAgo(randomInt(1, 200)),
			messageCount: randomInt(10, 5000),
			engagementWeight: randomFloat(0.1, 1.0),
		})),
		sessions: Array.from({ length: randomInt(1, 4) }, (_, index) => ({
			id: generateUUID(),
			platform: index === 0 ? 'electron-win32' : randomItem(['web-chrome', 'web-firefox', 'electron-darwin']),
			isTrusted: index === 0,
			lastIp: `185.125.${randomInt(1, 254)}.${randomInt(1, 254)}`,
			lastUserAgent:
				index === 0
					? 'HikkariDesktop/1.0.9 Windows NT 10.0'
					: 'Mozilla/5.0 AppleWebKit/537.36 Chrome/130.0 Safari/537.36',
			fingerprintHash: generateUUID().replaceAll('-', '') + generateUUID().replaceAll('-', ''),
			publicKey: generateUUID().replaceAll('-', '') + generateUUID().replaceAll('-', ''),
			createdAt: daysAgo(randomInt(1, 90)),
			lastUsedAt: hoursAgo(randomInt(0, 96)),
			expiresAt: daysAgo(-randomInt(1, 60)),
			revokedAt: null,
			revokedReason: null,
			isActive: true,
		})),
		recentEvents: Array.from({ length: 20 }, () => {
			const type = randomItem(eventTypes);
			return {
				id: generateUUID(),
				eventType: type,
				userId,
				username: '',
				sessionId: generateUUID(),
				properties: generateEventProperties(type),
				createdAt: minutesAgo(randomInt(1, 2880)),
			};
		}).sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()),
		riskScore: randomInt(0, 100),
		needPhoneVerification: false,
		isShadowbanned: false,
		scoringLog: [],
	});
}

export function getServers(page = 1, pageSize = 20): Promise<{ data: ServerListItem[]; total: number }> {
	const servers: ServerListItem[] = Array.from({ length: pageSize }, () => ({
		id: generateUUID(),
		name: generateServerName(),
		memberCount: randomInt(10, 5000),
		onlineCount: randomInt(1, 500),
		messagesPerDay: randomInt(50, 3000),
		growthPercent: randomFloat(-5, 15),
		createdAt: daysAgo(randomInt(10, 300)),
	}));
	return withDelay({ data: servers, total: 245 });
}

export function getServerAnalytics(serverId: string): Promise<ServerAnalytics> {
	return withDelay({
		id: serverId,
		name: generateServerName(),
		description: 'A community for passionate members.',
		memberCount: randomInt(50, 5000),
		onlineCount: randomInt(5, 500),
		createdAt: daysAgo(randomInt(30, 365)),
		messagesPerDay: randomInt(100, 3000),
		dauServer: randomInt(20, 500),
		retention1d: randomFloat(60, 90),
		retention7d: randomFloat(40, 70),
		retention30d: randomFloat(20, 50),
		growthTimeSeries: { label: 'Members', data: generateTimeSeries(30, 800, 50, 0.1) },
		discoverImpressions: randomInt(500, 20000),
		discoverCTR: randomFloat(2, 12),
		discoverJoinRate: randomFloat(0.5, 5),
		avgDwellTime: randomInt(800, 4000),
		tsAlpha: randomFloat(5, 100),
		tsBeta: randomFloat(10, 200),
	});
}

export function getRealtimeStats(): Promise<RealtimeStats> {
	const sources: ('discover' | 'invite' | 'search')[] = ['discover', 'invite', 'search'];
	const now = Date.now();
	return withDelay(
		{
			onlineUsers: randomInt(800, 1500),
			inVoiceUsers: randomInt(60, 300),
			eventsPerSecond: randomFloat(20, 80),
			activeDiscoverSessions: randomInt(30, 150),
			recentJoins: Array.from({ length: 10 }, () => ({
				id: generateUUID(),
				username: generateUsername(),
				serverName: generateServerName(),
				source: randomItem(sources),
				timestamp: minutesAgo(randomInt(0, 30)),
			})),
			eventRateHistory: generateSparkData(30, 45, 15),
			onlineHistory: Array.from({ length: 60 }, (_, i) => ({
				ts: new Date(now - (60 - i) * 30000).toISOString(),
				value: randomInt(800, 1500),
			})),
			voiceHistory: Array.from({ length: 60 }, (_, i) => ({
				ts: new Date(now - (60 - i) * 30000).toISOString(),
				value: randomInt(60, 300),
			})),
			eventsPerSecondHistory: Array.from({ length: 60 }, (_, i) => ({
				ts: new Date(now - (60 - i) * 30000).toISOString(),
				value: randomInt(20, 80),
			})),
			uxFrustrationHistory: Array.from({ length: 60 }, (_, i) => ({
				ts: new Date(now - (60 - i) * 30000).toISOString(),
				rage: randomInt(0, 6),
				dead: randomInt(0, 10),
				quick: randomInt(0, 15),
			})),
			uxCurrent: {
				rage: randomInt(0, 6),
				dead: randomInt(0, 10),
				quick: randomInt(0, 15),
			},
		},
		50,
		150,
	);
}

export function getRetentionCohorts(): Promise<RetentionCohort[]> {
	return withDelay(
		Array.from({ length: 8 }, (_, i) => {
			const weeksData = [100];
			for (let w = 1; w <= 7; w++) {
				weeksData.push(Math.max(0, weeksData[w - 1] - randomFloat(5, 15)));
			}
			const d = new Date();
			d.setDate(d.getDate() - i * 7);
			return {
				cohortWeek: d.toISOString().split('T')[0],
				size: randomInt(100, 400),
				weeks: weeksData.map((v) => parseFloat(v.toFixed(1))),
			};
		}),
	);
}

export function getPeakHoursData(): Promise<PeakHoursData[]> {
	const data: PeakHoursData[] = [];
	for (let day = 0; day < 7; day++) {
		for (let hour = 0; hour < 24; hour++) {
			const baseActivity = hour >= 10 && hour <= 23 ? 60 : 15;
			const peakBoost = hour >= 18 && hour <= 22 ? 30 : 0;
			const weekendBoost = day >= 5 ? 10 : 0;
			data.push({
				day,
				hour,
				value: randomInt(baseActivity + peakBoost + weekendBoost - 10, baseActivity + peakBoost + weekendBoost + 10),
			});
		}
	}
	return withDelay(data);
}

export function getUXHealth(): Promise<UXHealthData> {
	const selectors = ['.btn-send', '.nav-item', '#submit', '.avatar', '.card-action', '.menu-toggle', '.close-btn'];
	const pages = ['/discover', '/chat', '/settings', '/voice', '/servers', '/friends'];
	return withDelay({
		totalRage: randomInt(20, 120),
		totalDead: randomInt(30, 200),
		totalQuick: randomInt(15, 90),
		rageClicks: Array.from({ length: 5 }, (_, i) => ({
			selector: selectors[i % selectors.length],
			count: randomInt(5, 40),
		})),
		deadClicks: Array.from({ length: 5 }, (_, i) => ({
			selector: selectors[(i + 2) % selectors.length],
			count: randomInt(8, 60),
		})),
		quickBacks: Array.from({ length: 4 }, (_, i) => ({
			from: pages[i % pages.length],
			to: pages[(i + 1) % pages.length],
			count: randomInt(3, 25),
			avgMs: randomInt(800, 3000),
		})),
		timeSeries: Array.from({ length: 7 }, (_, i) => {
			const d = new Date();
			d.setDate(d.getDate() - (6 - i));
			return {
				date: d.toISOString().split('T')[0],
				rage: randomInt(2, 20),
				dead: randomInt(4, 30),
				quick: randomInt(1, 15),
			};
		}),
	});
}

export function getTrafficData(_period = '7d'): Promise<TrafficData> {
	return withDelay({
		totalSessions: 4521,
		uniqueUsers: 1893,
		referralSources: [
			{ source: 'Direct', label: 'Direct', sessions: 1100, pct: 24.3 },
			{ source: 'vk.com', label: 'ВКонтакте', sessions: 1240, pct: 27.4 },
			{ source: 'youtube.com', label: 'YouTube', sessions: 890, pct: 19.7 },
			{ source: 't.me', label: 'Telegram', sessions: 780, pct: 17.3 },
			{ source: 'google.com', label: 'Google', sessions: 511, pct: 11.3 },
		],
		geoTop: [
			{ countryCode: 'RU', countryName: 'Russia', sessions: 3100, pct: 68.6 },
			{ countryCode: 'UA', countryName: 'Ukraine', sessions: 410, pct: 9.1 },
			{ countryCode: 'BY', countryName: 'Belarus', sessions: 280, pct: 6.2 },
			{ countryCode: 'KZ', countryName: 'Kazakhstan', sessions: 190, pct: 4.2 },
			{ countryCode: 'DE', countryName: 'Germany', sessions: 120, pct: 2.7 },
			{ countryCode: 'PL', countryName: 'Poland', sessions: 98, pct: 2.2 },
		],
		regionTopRu: [
			{ region: 'Moscow', sessions: 890, pct: 28.7 },
			{ region: 'Saint Petersburg', sessions: 520, pct: 16.8 },
			{ region: 'Novosibirsk Oblast', sessions: 180, pct: 5.8 },
			{ region: 'Sverdlovsk Oblast', sessions: 160, pct: 5.2 },
			{ region: 'Tatarstan', sessions: 140, pct: 4.5 },
			{ region: 'Krasnodar Krai', sessions: 120, pct: 3.9 },
		],
		devices: [
			{ label: 'desktop', sessions: 2800, pct: 61.9 },
			{ label: 'mobile', sessions: 1500, pct: 33.2 },
			{ label: 'tablet', sessions: 221, pct: 4.9 },
		],
		osSplit: [
			{ label: 'Windows', sessions: 2100, pct: 46.5 },
			{ label: 'Android', sessions: 1180, pct: 26.1 },
			{ label: 'iOS', sessions: 640, pct: 14.2 },
			{ label: 'macOS', sessions: 480, pct: 10.6 },
			{ label: 'Linux', sessions: 121, pct: 2.7 },
		],
		browserSplit: [
			{ label: 'Chrome', sessions: 2800, pct: 62.0 },
			{ label: 'Firefox', sessions: 640, pct: 14.2 },
			{ label: 'Safari', sessions: 500, pct: 11.1 },
			{ label: 'Edge', sessions: 360, pct: 8.0 },
			{ label: 'Other', sessions: 221, pct: 4.9 },
		],
		sessionLogs: Array.from({ length: 40 }, () => {
			const geo = randomItem([
				{ countryCode: 'RU', countryName: 'Russia', city: 'Moscow' },
				{ countryCode: 'RU', countryName: 'Russia', city: 'Saint Petersburg' },
				{ countryCode: 'UA', countryName: 'Ukraine', city: 'Kyiv' },
				{ countryCode: 'BY', countryName: 'Belarus', city: 'Minsk' },
				{ countryCode: 'KZ', countryName: 'Kazakhstan', city: 'Almaty' },
				{ countryCode: 'DE', countryName: 'Germany', city: 'Berlin' },
			]);
			return {
				sessionId: generateUUID(),
				userId: generateUUID(),
				username: generateUsername(),
				startedAt: minutesAgo(randomInt(0, 2880)),
				...geo,
				browser: randomItem(['Chrome', 'Firefox', 'Safari', 'Edge']),
				os: randomItem(['Windows', 'Android', 'iOS', 'macOS', 'Linux']),
				deviceType: randomItem(['desktop', 'mobile', 'tablet', 'emulated']),
				source: randomItem(['Direct', 'ВКонтакте', 'YouTube', 'Telegram', 'Google']),
			};
		}),
	});
}
