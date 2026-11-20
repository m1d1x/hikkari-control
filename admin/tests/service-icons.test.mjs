import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFile, stat } from 'node:fs/promises';

// Every icon a group can carry is drawn as the service's own brand mark from
// `static/services/<id>.png` (components/product/ServiceLogo.svelte). The files
// are copies of the client's `app/assets/services`, so the admin shows the same
// real logo a tester sees in the app — no hand-drawn stand-ins.
const presets = await readFile(new URL('../src/lib/product/presets.ts', import.meta.url), 'utf8');

const iconIds = [...presets.matchAll(/\{ id: '([a-z0-9-]+)', label: /g)].map((m) => m[1]);

test('every catalog icon has its logo file in static/services', async () => {
	assert.ok(iconIds.length > 50, `only ${iconIds.length} icons parsed`);
	for (const id of iconIds) {
		const file = new URL(`../static/services/${id}.png`, import.meta.url);
		const info = await stat(file).catch(() => null);
		assert.ok(info, `static/services/${id}.png is missing`);
		assert.ok(info.size > 600, `static/services/${id}.png looks empty`);
		const head = (await readFile(file)).subarray(0, 8);
		assert.deepEqual(
			[...head],
			[0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a],
			`static/services/${id}.png is not a PNG`,
		);
	}
});

test('the logo component knows only ids that have a file', async () => {
	// A group may carry a hand-typed icon id; ServiceLogo must not guess a URL
	// for it from anything but SERVICE_ICONS.
	const source = await readFile(
		new URL('../src/components/product/ServiceLogo.svelte', import.meta.url),
		'utf8',
	);
	assert.match(source, /import \{ SERVICE_ICONS \} from '\$lib\/product\/presets'/);
	assert.match(source, /new Set\(SERVICE_ICONS\.map/);
});
