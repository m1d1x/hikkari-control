import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFile } from 'node:fs/promises';

// Hikkari is a protocol of its own beside naive: a box can be ordered as one,
// and every map that names a protocol on screen knows it.
const source = await readFile(new URL('../src/lib/api/entry.ts', import.meta.url), 'utf8');

test('hikkari is in the box protocols and in every protocol map', () => {
	assert.match(source, /type BoxProto = [^;]*'hikkari'/);
	const boxProtos = source.slice(source.indexOf('export const BOX_PROTOS'));
	assert.match(boxProtos.slice(0, boxProtos.indexOf('];')), /id: 'hikkari'/);
	for (const map of ['PROTO', 'PROTO_LABEL', 'PROTO_ROLE']) {
		const from = source.indexOf(`export const ${map}`);
		const body = source.slice(from, source.indexOf('};', from));
		assert.match(body, /hikkari/, `${map} has no hikkari`);
	}
});
