// Mock data for the product screens that have no backend yet: support
// tickets and donations. Metrics and errors are real (src/lib/api/product.ts,
// counted by the hub from client telemetry), presets and groups too
// (src/lib/api/presets.ts).

import { type Win } from './format';

export { WINDOWS, num, rub, pct, compact, bucketText, ago, type Win } from './format';

export type Platform = 'windows' | 'macos' | 'android';

const HOUR = 3600_000;
const DAY = 24 * HOUR;
const NOW = Date.now();

function rng(seed: number) {
	return () => {
		seed = (seed + 0x6d2b79f5) | 0;
		let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
		t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

const SPEC: Record<Win, { n: number; step: number }> = {
	'24h': { n: 24, step: HOUR },
	'7d': { n: 28, step: 6 * HOUR },
	'30d': { n: 30, step: DAY },
};

/** Bucket start times, oldest first. */
export function buckets(w: Win): number[] {
	const { n, step } = SPEC[w];
	const end = Math.floor(NOW / step) * step;
	return Array.from({ length: n }, (_, i) => end - (n - 1 - i) * step);
}

/** A per-day rate spread over the window's buckets: daily rhythm, slow growth, noise. */
function series(w: Win, perDay: number, seed: number): number[] {
	const r = rng(seed);
	const { step } = SPEC[w];
	return buckets(w).map((t) => {
		const hour = new Date(t).getHours();
		const rhythm = step < DAY ? 1 + 0.55 * Math.sin(((hour - 9) / 24) * 2 * Math.PI) : 1;
		const growth = 1 - ((NOW - t) / (30 * DAY)) * 0.35;
		return Math.max(0, Math.round(perDay * (step / DAY) * rhythm * growth * (0.8 + r() * 0.4)));
	});
}

const sum = (a: number[]) => a.reduce((s, v) => s + v, 0);

// ---------- support ----------

export type TicketStatus = 'open' | 'answered' | 'closed';

export interface Ticket {
	id: number;
	from: string;
	platform: Platform;
	version: string;
	status: TicketStatus;
	messages: { staff: boolean; text: string; ts: number }[];
}

export const tickets: Ticket[] = [
	{
		id: 214,
		from: '@kirill_v',
		platform: 'windows',
		version: '0.9.4',
		status: 'open',
		messages: [{ staff: false, text: 'после обновления не подключается, пишет что порт занят', ts: NOW - 25 * 60_000 }],
	},
	{
		id: 213,
		from: 'anon · 7f3a…c1',
		platform: 'macos',
		version: '0.9.4',
		status: 'open',
		messages: [
			{ staff: false, text: 'ютуб грузит, а дискорд голос не работает', ts: NOW - 2 * HOUR },
			{ staff: false, text: 'режим обход, профиль reality', ts: NOW - 2 * HOUR + 60_000 },
		],
	},
	{
		id: 211,
		from: 'mail@ivanova.me',
		platform: 'android',
		version: '0.9.3',
		status: 'answered',
		messages: [
			{ staff: false, text: 'как добавить свой сервер?', ts: NOW - 9 * HOUR },
			{ staff: true, text: 'профили → импорт, вставьте ссылку vless://', ts: NOW - 8 * HOUR },
		],
	},
	{
		id: 207,
		from: '@den',
		platform: 'windows',
		version: '0.9.3',
		status: 'closed',
		messages: [
			{ staff: false, text: 'антивирус ругается на wintun.dll', ts: NOW - 3 * DAY },
			{ staff: true, text: 'это драйвер туннеля, добавьте папку в исключения', ts: NOW - 3 * DAY + HOUR },
			{ staff: false, text: 'помогло, спасибо', ts: NOW - 3 * DAY + 2 * HOUR },
		],
	},
];

// ---------- donations ----------

export interface Donation {
	ts: number;
	who: string | null;
	rub: number;
	via: string;
	note: string;
}

export function donations(w: Win) {
	const sums = series(w, 4200, 11);
	const r = rng(12);
	const VIA = ['boosty', 'tribute', 'crypto'];
	const NOTES = ['', '', 'спасибо за обход', '', 'на сервера', '', 'лучший впн'];
	const list: Donation[] = Array.from({ length: 12 }, (_, i) => ({
		ts: NOW - Math.round((i + r()) * (w === '24h' ? 1.5 : w === '7d' ? 12 : 50) * HOUR),
		who: r() < 0.4 ? null : ['@mira', '@stas', 'oleg k.', '@nyan', '@vova', 'tanya'][i % 6],
		rub: [100, 200, 300, 500, 1000, 250][Math.floor(r() * 6)],
		via: VIA[Math.floor(r() * VIA.length)],
		note: NOTES[i % NOTES.length],
	}));
	const total = sum(sums);
	const count = Math.max(1, Math.round(total / 340));
	const days = SPEC[w].n * (SPEC[w].step / DAY);
	return {
		buckets: buckets(w),
		series: sums,
		total,
		count,
		avg: total / count,
		donors: Math.round(count * 0.8),
		monthForecast: (total / days) * 30,
		list,
	};
}
