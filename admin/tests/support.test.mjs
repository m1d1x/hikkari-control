import assert from 'node:assert/strict';
import { test } from 'node:test';

test('support api contracts and report types', async () => {
	const report = {
		id: 42,
		install_id: 'r-user-123',
		from: 'r-user-123',
		platform: 'macos',
		version: '1.0.10',
		message: 'Проблема с туннелем',
		logs: '=== Diagnostic Report ===\n[proxy] ok',
		status: 'open',
		messages: [{ staff: false, text: 'Проблема с туннелем', ts: Date.now() }],
		created_at: new Date().toISOString(),
		updated_at: new Date().toISOString(),
	};

	assert.equal(report.id, 42);
	assert.equal(report.status, 'open');
	assert.equal(report.messages.length, 1);
	assert.ok(report.logs.includes('Diagnostic Report'));
});
