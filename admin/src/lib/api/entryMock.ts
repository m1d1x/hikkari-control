import type { EntryOverview, OverviewWindow } from './entry';

// Fixture for `VITE_USE_MOCK=true`: only what the overview screen draws, with
// money figures of the same order as the live hub's. Deterministic.

const SHAPE: Record<OverviewWindow, { n: number; step: number }> = {
	'24h': { n: 24, step: 3600 },
	'7d': { n: 28, step: 6 * 3600 },
	'30d': { n: 30, step: 86400 },
};

const line = (cents: number) => ({ perDayCents: cents, monthCents: cents * 30, projectionCents: cents * 30 });

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
			gbPerPersonDay: +(((down + up) / days) / 5).toFixed(3),
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
			{ proto: 'vless-reality-vision', attempts: 410, sharePct: 52.4, p50Ms: 380, p95Ms: 1150, connectP50Ms: 240, latency: latency(380) },
			{ proto: 'vless-xhttp', attempts: 230, sharePct: 29.4, p50Ms: 460, p95Ms: 1400, connectP50Ms: 310, latency: latency(460) },
			{ proto: 'hysteria2', attempts: 142, sharePct: 18.2, p50Ms: 290, p95Ms: 900, connectP50Ms: 180, latency: latency(290) },
		],
		traffic: {
			gbTotal: +(gbDown + gbUp).toFixed(2),
			gbUp: +gbUp.toFixed(2),
			gbDown: +gbDown.toFixed(2),
			gbPerPersonDay: 0.42,
			relaysReporting: 6,
			series: traffic,
		},
		trafficPerUser: { people: 5, medianMbDay: 310, p90MbDay: 1240, viaRelayPeople: 4, viaRelayMedianMbDay: 180, viaRelayPct: 38.5 },
		needsYou: [],
		automation: [],
		missing: [],
	};
}
