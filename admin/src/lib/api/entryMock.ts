import type { EntryFleet, EntryOverview, FleetBox, FleetRelay, OverviewWindow, ProviderStats } from './entry';

// Fixture for `VITE_USE_MOCK=true`: only what the overview and fleet screens draw, with
// money figures of the same order as the live hub's. Deterministic.

const SHAPE: Record<OverviewWindow, { n: number; step: number }> = {
	'24h': { n: 24, step: 3600 },
	'7d': { n: 28, step: 6 * 3600 },
	'30d': { n: 30, step: 86400 },
};

const line = (cents: number) => ({
	perDayCents: cents,
	monthCents: cents * 30,
	projectionCents: cents * 30,
	rateMonthCents: cents * 30,
});

export function mockOverview(window: OverviewWindow): EntryOverview {
	const { n, step } = SHAPE[window];
	const end = Math.floor(Date.now() / 1000 / step) * step;
	const series = Array.from({ length: n }, (_, i) => {
		const bucket = new Date((end - (n - 1 - i) * step) * 1000).toISOString();
		const wave = Math.sin(i / 3);
		return {
			bucket,
			machinesPerDayCents: 56,
			// one purchase now and then, otherwise nothing
			domainsCents: i % 11 === 6 ? 1176 : 0,
			totalCents: 56,
			people: 5,
			perUserMonthCents: 420 + Math.round(wave * 60),
			trendTotalPerDayCents: 233,
			trendPerUserMonthCents: 1398 + Math.round(wave * 40),
		};
	});
	const stamp = (i: number) => new Date((end - (n - 1 - i) * step) * 1000).toISOString();
	const days = step / 86400;
	const traffic = Array.from({ length: n }, (_, i) => {
		const down = (0.55 + 0.25 * Math.sin(i / 2.5) + (i % 5) * 0.03) * days * 4;
		const up = down * 0.18;
		return {
			bucket: stamp(i),
			gbDown: +down.toFixed(3),
			gbUp: +up.toFixed(3),
			gbPerPersonDay: +((down + up) / days / 5).toFixed(3),
			trendGbPerPersonDay: +(0.42 + 0.03 * Math.sin(i / 4)).toFixed(3),
		};
	});
	const gbDown = traffic.reduce((a, p) => a + p.gbDown, 0);
	const gbUp = traffic.reduce((a, p) => a + p.gbUp, 0);
	const survival = Array.from({ length: n }, (_, i) => ({
		bucket: stamp(i),
		deaths: i % 7 === 3 ? 1 : 0,
		boxMedianDays: i % 7 === 3 ? 11 + (i % 4) : null,
		relayMedianDays: i % 7 === 3 ? 3.5 + (i % 3) * 0.6 : null,
		domainMedianDays: i % 9 === 4 ? 1.2 + (i % 3) * 0.4 : null,
	}));
	const latency = (base: number) =>
		Array.from({ length: n }, (_, i) => ({
			bucket: stamp(i),
			p50Ms: Math.round(base + 30 * Math.sin(i / 3 + base)),
			attempts: 20 + (i % 6) * 4,
		}));
	return {
		window,
		bucketSeconds: step,
		reach: { attempts: 0, people: 0, firstTryPct: 0, retriedPct: 0, failedPct: 0, series: [] },
		money: {
			domains: line(176),
			machines: line(56),
			total: line(233),
			monthlyCapCents: 3000,
			machinesUnpriced: 0,
			machinesPriced: 3,
			perUserMonthCents: 1398,
			activePeople: 5,
			series,
		},
		survival: {
			boxMedianDays: 12,
			boxMedianDaysBefore: 9.5,
			censorDeaths: survival.reduce((a, p) => a + p.deaths, 0),
			domainMedianHours: 31,
			domainMedianHoursBefore: 40,
			relayMedianDays: 4.2,
			relayMedianDaysBefore: 3.6,
			relaysEver: 23,
			series: survival,
		},
		protocols: [
			{
				proto: 'vless-reality-vision',
				attempts: 410,
				sharePct: 52.4,
				p50Ms: 380,
				p95Ms: 1150,
				connectP50Ms: 240,
				latency: latency(380),
			},
			{
				proto: 'vless-xhttp',
				attempts: 230,
				sharePct: 29.4,
				p50Ms: 460,
				p95Ms: 1400,
				connectP50Ms: 310,
				latency: latency(460),
			},
			{
				proto: 'hysteria2',
				attempts: 142,
				sharePct: 18.2,
				p50Ms: 290,
				p95Ms: 900,
				connectP50Ms: 180,
				latency: latency(290),
			},
		],
		traffic: {
			gbTotal: +(gbDown + gbUp).toFixed(2),
			gbUp: +gbUp.toFixed(2),
			gbDown: +gbDown.toFixed(2),
			gbPerPersonDay: 0.42,
			relaysReporting: 6,
			series: traffic,
		},
		trafficPerUser: {
			people: 5,
			medianMbDay: 310,
			p90MbDay: 1240,
			viaRelayPeople: 4,
			viaRelayMedianMbDay: 180,
			viaRelayPct: 38.5,
		},
		needsYou: [],
		automation: [],
		missing: [],
	};
}

/** The fleet screen as the live hub had it on 19.11: six machines at four hosters, two hosters with none left. */
export function mockFleet(window: OverviewWindow): EntryFleet {
	const { n, step } = SHAPE[window];
	const end = Math.floor(Date.now() / 1000 / step) * step;
	const stamp = (i: number) => new Date((end - (n - 1 - i) * step) * 1000).toISOString();
	const ago = (days: number) => new Date(Date.now() - days * 86400_000).toISOString();
	const relay = (name: string, grants: number): FleetRelay => ({
		name,
		proto: 'hikkari',
		port: 443,
		cover: '',
		grants,
		signedAt: ago(3),
		status: 'alive',
		verdict: 'ok',
		kind: 'personal',
	});
	const box = (
		name: string,
		provider: string,
		location: string,
		days: number,
		cents: number | undefined,
		relays: FleetRelay[],
		extra: Partial<FleetBox> = {},
	): FleetBox => ({
		name,
		provider,
		location,
		proto: 'hikkari',
		createdAt: ago(days),
		verdict: 'ok',
		verdictLabel: '',
		relays,
		monthlyCents: cents,
		...extra,
	});
	const boxes: FleetBox[] = [
		box('aezamsk1', 'aeza', 'Moscow, RU', 8, 1700, [], {
			label: 'вход',
			openIncident: 'пул входа просел',
		}),
		box('aeznl1', 'aeza', 'Amsterdam, NL', 3, undefined, [], { label: 'выход' }),
		box('unl1', 'u1', 'Amsterdam, NL', 12, 586, [relay('unl1-2', 8)], {
			label: 'приёмник',
			openIncident: 'тишина магистрали',
		}),
		box('ams3', 'vdsina', 'Amsterdam, NL', 9, 210, [relay('ams3-3', 6)], { openIncident: 'steal 8%' }),
		box('vtg-spb', 'vantage', 'Saint Petersburg, RU', 20, 915, [], { label: 'пробник' }),
		box('vtg-msk', 'vantage', 'Moscow, RU', 20, 915, []),
	];
	const stats = (p: Partial<ProviderStats> & { provider: string }): ProviderStats => ({
		boughtCount: 0,
		aliveCount: 0,
		retiredCount: 0,
		bannedCount: 0,
		rotatedCount: 0,
		cohortCount: 0,
		survivedCount: 0,
		winratePct: null,
		medianLifeDays: null,
		lifeSampleCount: 0,
		monthlyCents: 0,
		perBoxMonthlyCents: null,
		pricelessBoxes: 0,
		costPerLivingDayCents: null,
		bansDaily: [],
		bansWindow: 0,
		trafficGb: null,
		planTb: null,
		spentCents: 0,
		...p,
	});
	return {
		window,
		bucketSeconds: step,
		pool: {
			inDistribution: 2,
			relaysCount: 2,
			boxesCount: 6,
			pendingCount: 0,
			rollingCount: 0,
			series: Array.from({ length: n }, (_, i) => ({ bucket: stamp(i), size: 2, boxes: 6 })),
			// entry.ts declares FleetPool twice and the two merge; the fleet screen reads only these
		} as EntryFleet['pool'],
		strip: {
			deathsVsReplacements: {
				deathsDay: 0,
				replacementsDay: 0,
				series: Array.from({ length: n }, (_, i) => ({ bucket: stamp(i), deaths: 0, replacements: 0 })),
			},
			timeToReplacement: { medianSec: null, peakSec: null, sampleCount: 0 },
		},
		jobs: [],
		boxes,
		providers: {
			windowDays: 30,
			missing: [],
			hosting: [
				stats({
					provider: 'aeza',
					boughtCount: 2,
					aliveCount: 2,
					monthlyCents: 1700,
					perBoxMonthlyCents: 1700,
					pricelessBoxes: 1,
					trafficGb: 0.02,
					spentCents: 391,
				}),
				stats({ provider: 'vantage', boughtCount: 2, aliveCount: 2, monthlyCents: 1830, perBoxMonthlyCents: 915 }),
				stats({
					provider: 'u1',
					label: 'u1 нидерланды',
					boughtCount: 1,
					aliveCount: 1,
					monthlyCents: 586,
					perBoxMonthlyCents: 586,
					trafficGb: 565,
					spentCents: 223,
				}),
				stats({
					provider: 'vdsina',
					boughtCount: 6,
					aliveCount: 1,
					retiredCount: 5,
					bannedCount: 2,
					monthlyCents: 210,
					perBoxMonthlyCents: 210,
					medianLifeDays: 0.01,
					lifeSampleCount: 2,
					trafficGb: 550,
					spentCents: 120,
				}),
				stats({
					provider: 'openstack',
					boughtCount: 3,
					retiredCount: 3,
					bannedCount: 1,
					perBoxMonthlyCents: 299,
					medianLifeDays: 0.01,
					lifeSampleCount: 1,
					trafficGb: 221,
					spentCents: 524,
				}),
				stats({ provider: 'standalone', boughtCount: 1, retiredCount: 1 }),
			],
		},
		cleanBoxesCount: 3,
		cleanRelaysCount: 2,
		retiredBoxesCount: 9,
		missing: [],
	};
}
