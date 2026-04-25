import './mock-vscode'; // must be imported first to intercept 'vscode' require

import * as assert from 'node:assert';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { resetMockState, mockConfigValues } from './mock-vscode';
import { logger, _setLoggerForTests, _getInternalChannel } from '../logger';

// Minimal fake channel for test injection.
interface FakeChannel {
	lines: string[];
	appendLine(line: string): void;
	appendLineCallCount: number;
	failing: boolean;
}

function createFakeChannel(): FakeChannel {
	const ch: FakeChannel = {
		lines: [],
		appendLineCallCount: 0,
		failing: false,
		appendLine(line: string) {
			ch.appendLineCallCount++;
			if (ch.failing) { throw new Error('channel write failure'); }
			ch.lines.push(line);
		},
	};
	return ch;
}

describe('logger', () => {
	let ch: FakeChannel;

	beforeEach(() => {
		resetMockState();
		ch = createFakeChannel();
		_setLoggerForTests(ch);
	});

	afterEach(() => {
		// Reset verbose flag between tests.
		delete mockConfigValues['mcpGateway.verboseLogging'];
	});

	it('info writes a line containing source and message', () => {
		logger.info('test-src', 'hello world');
		assert.strictEqual(ch.lines.length, 1);
		assert.ok(ch.lines[0].includes('[test-src]'), 'source in line');
		assert.ok(ch.lines[0].includes('hello world'), 'message in line');
		assert.ok(ch.lines[0].includes('[INFO]'), 'level in line');
	});

	it('warn writes a line containing source and message', () => {
		logger.warn('warn-src', 'something off');
		assert.strictEqual(ch.lines.length, 1);
		assert.ok(ch.lines[0].includes('[warn-src]'));
		assert.ok(ch.lines[0].includes('something off'));
		assert.ok(ch.lines[0].includes('[WARN]'));
	});

	it('error writes a line containing source and message', () => {
		logger.error('err-src', 'something broke');
		assert.strictEqual(ch.lines.length, 1);
		assert.ok(ch.lines[0].includes('[err-src]'));
		assert.ok(ch.lines[0].includes('something broke'));
		assert.ok(ch.lines[0].includes('[ERROR]'));
	});

	it('warn with Error appends Error.message on a new line', () => {
		logger.warn('w', 'bad thing', new Error('detail message'));
		assert.strictEqual(ch.lines.length, 1);
		const line = ch.lines[0];
		assert.ok(line.includes('  Error: detail message'), `expected error detail, got: ${line}`);
	});

	it('error with Error appends Error.message on a new line', () => {
		logger.error('e', 'oops', new Error('root cause'));
		assert.strictEqual(ch.lines.length, 1);
		const line = ch.lines[0];
		assert.ok(line.includes('  Error: root cause'), `expected error detail, got: ${line}`);
	});

	it('error with non-Error appends String(err) on a new line', () => {
		logger.error('e', 'oops', 'some string error');
		const line = ch.lines[0];
		assert.ok(line.includes('  Error: some string error'), `expected string error, got: ${line}`);
	});

	it('debug is skipped when verboseLogging is false (default)', () => {
		mockConfigValues['mcpGateway.verboseLogging'] = false;
		logger.debug('d', 'verbose message');
		assert.strictEqual(ch.lines.length, 0, 'debug line should not be emitted when verbose=false');
	});

	it('debug emits when verboseLogging is true', () => {
		mockConfigValues['mcpGateway.verboseLogging'] = true;
		logger.debug('d', 'verbose message');
		assert.strictEqual(ch.lines.length, 1);
		assert.ok(ch.lines[0].includes('verbose message'));
		assert.ok(ch.lines[0].includes('[DEBUG]'));
	});

	it('_setLoggerForTests injects channel; appendLine call count reflects writes', () => {
		const localCh = createFakeChannel();
		_setLoggerForTests(localCh);
		logger.info('x', 'msg1');
		logger.warn('x', 'msg2');
		assert.strictEqual(localCh.appendLineCallCount, 2);
	});

	it('timestamp format matches ISO 8601', () => {
		logger.info('t', 'ts test');
		// ISO: 2026-04-25T20:42:36.765Z
		const iso = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z/;
		assert.ok(iso.test(ch.lines[0]), `expected ISO timestamp, got: ${ch.lines[0]}`);
	});

	it('logger swallows internal channel throw — does not propagate to caller', () => {
		ch.failing = true;
		_setLoggerForTests(ch);
		// Must not throw.
		assert.doesNotThrow(() => {
			logger.info('safe', 'should not throw');
		});
	});

	it('_getInternalChannel returns the injected channel', () => {
		const localCh = createFakeChannel();
		_setLoggerForTests(localCh);
		assert.strictEqual(_getInternalChannel(), localCh);
	});
});
