import assert from 'node:assert/strict';
import { after, test } from 'node:test';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { compile } from 'svelte/compiler';
import { Window } from 'happy-dom';

const window = new Window();
for (const key of [
	'window',
	'document',
	'navigator',
	'HTMLElement',
	'Element',
	'Node',
	'Text',
	'Comment',
	'Event',
	'HTMLInputElement',
	'HTMLFormElement',
	'HTMLMediaElement',
	'MutationObserver',
]) {
	Object.defineProperty(globalThis, key, { value: key === 'window' ? window : window[key], configurable: true });
}
const { mount, unmount, flushSync } = await import('svelte');
const dir = await mkdtemp(new URL('../node_modules/.spend-test-', import.meta.url));
const source = await readFile(new URL('../src/components/entry/SpendCapsCard.svelte', import.meta.url), 'utf8');
await writeFile(`${dir}/Card.mjs`, compile(source, { filename: 'SpendCapsCard.svelte', generate: 'client' }).js.code);
await writeFile(
	`${dir}/Harness.mjs`,
	compile(
		`
<script>
 import Card from './Card.mjs';
 let { readings, onsave } = $props();
 let rows = $state(readings);
 let busy = $state(false);
 export function refresh(next) { rows = next; }
 export function setBusy(next) { busy = next; }
</script>
{#each rows as reading (reading.role)}
 <Card {reading} {busy} saved={false} onsave={(daily, monthly) => onsave(reading.role, daily, monthly)} />
{/each}
`,
		{ filename: 'Harness.svelte', generate: 'client' },
	).js.code,
);
const { default: Harness } = await import(pathToFileURL(`${dir}/Harness.mjs`));
const stub = compile(
	`<script>let { option, height } = $props();</script><div class="echart" data-option={JSON.stringify(option)} style:height></div>`,
	{ filename: 'EChart.svelte', generate: 'client' },
).js.code;
await writeFile(`${dir}/EChart.mjs`, stub);
for (const name of ['AutopilotCharts', 'ui/TimeSeries']) {
	const code = (await readFile(new URL(`../src/components/entry/${name}.svelte`, import.meta.url), 'utf8')).replace(
		"'$components/charts/EChart.svelte'",
		"'./EChart.mjs'",
	);
	await writeFile(
		`${dir}/${name.replace('/', '-')}.mjs`,
		compile(code, { filename: `${name}.svelte`, generate: 'client' }).js.code,
	);
}

after(async () => {
	await rm(dir, { recursive: true });
	window.happyDOM.abort();
});

const readings = () =>
	['hosting', 'registrar'].map((role) => ({
		role,
		dailyCapCents: 100,
		monthlyCapCents: 1000,
		dayCents: 0,
		monthCents: 0,
		dayProjectedCents: 0,
		monthProjectedCents: 0,
	}));
function setup() {
	const target = document.createElement('div');
	document.body.append(target);
	const calls = [];
	let app;
	flushSync(() => {
		app = mount(Harness, {
			target,
			props: {
				readings: readings(),
				onsave: async (...args) => {
					calls.push(args);
				},
			},
		});
	});
	const inputs = [...target.querySelectorAll('input')];
	const fill = (i, value) =>
		flushSync(() => {
			inputs[i].value = value;
			inputs[i].dispatchEvent(new window.Event('input', { bubbles: true }));
		});
	const submit = (i) =>
		flushSync(() =>
			target.querySelectorAll('form')[i].dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true })),
		);
	return {
		target,
		app,
		calls,
		inputs,
		fill,
		submit,
		cleanup: async () => {
			await unmount(app);
			target.remove();
		},
	};
}

for (const first of [0, 1]) {
	test(`four edited limits survive saving ${first === 0 ? 'hosting' : 'registrar'} first`, async () => {
		const t = setup();
		try {
			['2', '20', '3', '30'].forEach((v, i) => t.fill(i, v));
			t.submit(first);
			const next = readings();
			next[first] = {
				...next[first],
				dailyCapCents: first === 0 ? 200 : 300,
				monthlyCapCents: first === 0 ? 2000 : 3000,
			};
			flushSync(() => t.app.refresh(next));
			assert.deepEqual(
				t.inputs.map((i) => i.value),
				['2', '20', '3', '30'],
			);
			t.submit(1 - first);
			assert.deepEqual(
				t.calls,
				first === 0
					? [
							['hosting', 200, 2000],
							['registrar', 300, 3000],
						]
					: [
							['registrar', 300, 3000],
							['hosting', 200, 2000],
						],
			);
		} finally {
			await t.cleanup();
		}
	});
}

test('failed save refresh retains draft; untouched card follows server', async () => {
	const t = setup();
	try {
		t.fill(0, '4');
		t.fill(1, '40');
		const next = readings();
		next[1] = { ...next[1], dailyCapCents: 500, monthlyCapCents: 5000 };
		flushSync(() => t.app.refresh(next));
		assert.deepEqual(
			t.inputs.map((i) => i.value),
			['4', '40', '5', '50'],
		);
		assert.match(t.target.textContent, /Есть несохранённые изменения/);
	} finally {
		await t.cleanup();
	}
});

test('invalid values and submissions during a save do not call API', async () => {
	const t = setup();
	try {
		t.fill(0, '20');
		t.fill(1, '2');
		t.submit(0);
		t.fill(0, '-1');
		t.submit(0);
		t.fill(0, '');
		t.submit(0);
		t.fill(0, '1');
		t.fill(1, '5');
		flushSync(() => t.app.setBusy(true));
		t.submit(0);
		assert.equal(t.calls.length, 0);
	} finally {
		await t.cleanup();
	}
});

test('autopilot keeps empty charts and uses yellow dashed forecasts in both units', async () => {
	const { default: Charts } = await import(pathToFileURL(`${dir}/AutopilotCharts.mjs`));
	for (const moneyMode of [true, false]) {
		const target = document.createElement('div');
		document.body.append(target);
		let app;
		flushSync(() => {
			app = mount(Charts, { target, props: { moneyMode, usesDomains: true } });
		});
		try {
			const charts = [...target.querySelectorAll('.echart')];
			assert.equal(charts.length, 3);
			for (const chart of charts) {
				const option = JSON.parse(chart.dataset.option);
				assert.equal(option.xAxis.type, 'time');
				assert.equal(option.yAxis.min, 0);
				assert.deepEqual(option.series[0].data, []);
				assert.deepEqual(option.series[1].data, []);
				assert.equal(option.series[1].lineStyle.color, '#facc15');
				assert.equal(option.series[1].lineStyle.type, 'dashed');
			}
		} finally {
			await unmount(app);
			target.remove();
		}
	}
});

test('missing time-series data keeps axes and does not invent zero measurements', async () => {
	const { default: Chart } = await import(pathToFileURL(`${dir}/ui-TimeSeries.mjs`));
	const target = document.createElement('div');
	document.body.append(target);
	let app;
	flushSync(() => {
		app = mount(Chart, {
			target,
			props: { labels: ['10:00', '11:00'], series: [{ name: 'load', color: '#06b6d4', vals: [null, null] }] },
		});
	});
	try {
		const option = JSON.parse(target.querySelector('.echart').dataset.option);
		assert.deepEqual(option.series[0].data, [null, null]);
		assert.deepEqual(option.xAxis.data, ['10:00', '11:00']);
		assert.match(target.textContent, /нет данных/);
	} finally {
		await unmount(app);
		target.remove();
	}
});
