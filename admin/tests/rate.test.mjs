import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';

const source = await readFile(new URL('../src/lib/utils/rate.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText;
const { formatRate } = await import(`data:text/javascript,${encodeURIComponent(js)}`);

// The same table as internal/entry/rate_test.go: hub and admin print alike.
test('formatRate picks the unit by size', () => {
	for (const [v, want] of [
		[0, '0 Мбит/с'],
		[0.15, '150 кбит/с'],
		[0.003, '3 кбит/с'],
		[0.0123, '12,3 кбит/с'],
		[0.0004, '0,4 кбит/с'],
		[0.9996, '1 Мбит/с'],
		[1, '1 Мбит/с'],
		[2.4, '2,4 Мбит/с'],
		[2.456, '2,46 Мбит/с'],
		[15.34, '15,3 Мбит/с'],
		[63, '63 Мбит/с'],
		[150.4, '150 Мбит/с'],
		[999.7, '1 Гбит/с'],
		[1250, '1,25 Гбит/с'],
		[-0.3, '−300 кбит/с'],
		[NaN, '- Мбит/с'],
	]) {
		assert.equal(formatRate(v), want, `formatRate(${v})`);
	}
});

test('formatRate signed writes the plus', () => {
	assert.equal(formatRate(1.2, true), '+1,2 Мбит/с');
	assert.equal(formatRate(-0.3, true), '−300 кбит/с');
	assert.equal(formatRate(0, true), '0 Мбит/с');
});
