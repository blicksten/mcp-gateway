/**
 * Tests for DaemonLogFile — Group B regression tests for B-NEW-32.
 *
 * All filesystem operations use the injectable fsImpl to avoid touching
 * the real filesystem. Date-rotation tests use the injectable clock.
 */

import './mock-vscode'; // must be imported first to intercept 'vscode' require

import * as assert from 'node:assert';
import * as path from 'node:path';
import { describe, it, beforeEach, afterEach } from 'mocha';
import { EventEmitter } from 'node:events';
import { DaemonLogFile } from '../daemon-log-file';

// ---------------------------------------------------------------------------
// Minimal fake fs implementation
// ---------------------------------------------------------------------------

interface FakeFile {
	content: string;
	mtimeMs: number;
}

/**
 * In-memory fake filesystem for tests. Provides just the subset of `fs`
 * used by DaemonLogFile: mkdirSync, createWriteStream, readdirSync,
 * statSync, unlinkSync.
 */
function createFakeFs(opts?: { mkdirFails?: boolean }) {
	const files = new Map<string, FakeFile>();
	const mkdirFails = opts?.mkdirFails ?? false;
	let mkdirCalled = false;
	let mkdirCallCount = 0;

	const fakeFs = {
		// State accessors for test assertions.
		files,
		get mkdirCalled() { return mkdirCalled; },
		get mkdirCallCount() { return mkdirCallCount; },

		mkdirSync(_p: string, _options?: unknown): void {
			mkdirCalled = true;
			mkdirCallCount++;
			if (mkdirFails) { throw Object.assign(new Error('EACCES: permission denied'), { code: 'EACCES' }); }
		},

		createWriteStream(filePath: string, _options?: unknown) {
			if (!files.has(filePath)) {
				files.set(filePath, { content: '', mtimeMs: Date.now() });
			}
			const file = files.get(filePath)!;
			const emitter = new EventEmitter();
			const stream = Object.assign(emitter, {
				write(chunk: string) {
					file.content += chunk;
					file.mtimeMs = Date.now();
					return true;
				},
				end() {},
				destroyed: false,
			});
			return stream as unknown as ReturnType<typeof import('node:fs').createWriteStream>;
		},

		readdirSync(dir: string): string[] {
			// Normalize dir so comparisons work across platforms (forward vs back slashes).
			const normalizedDir = path.normalize(dir);
			const results: string[] = [];
			for (const filePath of files.keys()) {
				const normalizedFile = path.normalize(filePath);
				const dirPart = path.dirname(normalizedFile);
				if (dirPart === normalizedDir) {
					results.push(path.basename(normalizedFile));
				}
			}
			return results;
		},

		statSync(filePath: string): { mtimeMs: number } {
			const file = files.get(filePath);
			if (!file) { throw Object.assign(new Error(`ENOENT: ${filePath}`), { code: 'ENOENT' }); }
			return { mtimeMs: file.mtimeMs };
		},

		unlinkSync(filePath: string): void {
			files.delete(filePath);
		},
	};

	return fakeFs as unknown as typeof import('node:fs') & typeof fakeFs;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const TEST_DIR = '/tmp/mcp-gateway-test-logs';

function makeDate(iso: string): Date {
	return new Date(iso);
}

function daysAgo(n: number): Date {
	return new Date(Date.now() - n * 24 * 60 * 60 * 1000);
}

// ---------------------------------------------------------------------------
// Group B — DaemonLogFile unit tests
// ---------------------------------------------------------------------------

describe('DaemonLogFile', () => {
	let logFile: DaemonLogFile;
	let fakeFs: ReturnType<typeof createFakeFs>;

	beforeEach(() => {
		fakeFs = createFakeFs();
	});

	afterEach(() => {
		if (logFile) { logFile.dispose(); }
	});

	// B1 — writeStdout appends to today's file with [stdout] tag
	it('B1: writeStdout appends to today log file with [stdout] tag', () => {
		const now = makeDate('2026-05-08T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		logFile.writeStdout('hello world');

		const expectedFile = path.join(TEST_DIR, 'daemon-2026-05-08.log');
		assert.ok(fakeFs.files.has(expectedFile), `Expected log file to exist: ${expectedFile}`);
		const content = fakeFs.files.get(expectedFile)!.content;
		assert.ok(content.includes('[stdout] hello world'), `Expected [stdout] tag, got: ${content}`);
	});

	// B2 — writeStderr appends to today's file with [stderr] tag
	it('B2: writeStderr appends to today log file with [stderr] tag', () => {
		const now = makeDate('2026-05-08T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		logFile.writeStderr('error output');

		const expectedFile = path.join(TEST_DIR, 'daemon-2026-05-08.log');
		assert.ok(fakeFs.files.has(expectedFile));
		const content = fakeFs.files.get(expectedFile)!.content;
		assert.ok(content.includes('[stderr] error output'), `Expected [stderr] tag, got: ${content}`);
	});

	// B3 — writeEvent appends with [event] tag
	it('B3: writeEvent appends with [event] tag', () => {
		const now = makeDate('2026-05-08T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		logFile.writeEvent('spawn: mcp-gateway');

		const expectedFile = path.join(TEST_DIR, 'daemon-2026-05-08.log');
		assert.ok(fakeFs.files.has(expectedFile));
		const content = fakeFs.files.get(expectedFile)!.content;
		assert.ok(content.includes('[event] spawn: mcp-gateway'), `Expected [event] tag, got: ${content}`);
	});

	// B4 — enabled=false → no writes
	it('B4: enabled=false produces no files', () => {
		const now = makeDate('2026-05-08T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: false,
			clock: () => now,
			fsImpl: fakeFs,
		});

		logFile.writeStdout('this should not be written');
		logFile.writeStderr('nor this');
		logFile.writeEvent('nor this either');

		assert.strictEqual(fakeFs.files.size, 0, 'No files should be created when disabled');
	});

	// B5 — daily rotation: advancing the clock date opens a new file
	it('B5: daily rotation opens new file when date advances', () => {
		let currentDate = makeDate('2026-01-01T12:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => currentDate,
			fsImpl: fakeFs,
		});

		logFile.writeStdout('day one write');

		// Advance to the next day
		currentDate = makeDate('2026-01-02T12:00:00.000Z');
		logFile.writeStdout('day two write');

		const day1File = path.join(TEST_DIR, 'daemon-2026-01-01.log');
		const day2File = path.join(TEST_DIR, 'daemon-2026-01-02.log');

		assert.ok(fakeFs.files.has(day1File), 'Day 1 file must exist');
		assert.ok(fakeFs.files.has(day2File), 'Day 2 file must exist');

		const day1Content = fakeFs.files.get(day1File)!.content;
		const day2Content = fakeFs.files.get(day2File)!.content;
		assert.ok(day1Content.includes('day one write'));
		assert.ok(day2Content.includes('day two write'));
	});

	// B6 — rotate() deletes files older than retentionDays
	it('B6: rotate() deletes files older than retentionDays', () => {
		const now = makeDate('2026-05-08T12:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 3,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		// Pre-populate files: 5 old (>3 days) + 3 recent (<=3 days)
		const oldDates = ['2026-04-28', '2026-04-29', '2026-04-30', '2026-05-01', '2026-05-02'];
		const recentDates = ['2026-05-05', '2026-05-06', '2026-05-07'];

		for (const dateStr of oldDates) {
			// Use path.join (no extra normalize) to match the key format that
			// DaemonLogFile produces via path.join in unlinkSync — consistent
			// with how the fake fs's unlinkSync deletes keys.
			const filePath = path.join(TEST_DIR, `daemon-${dateStr}.log`);
			fakeFs.files.set(filePath, {
				content: `log for ${dateStr}`,
				// mtime more than 3 days ago from 2026-05-08
				mtimeMs: new Date(`${dateStr}T12:00:00.000Z`).getTime(),
			});
		}
		for (const dateStr of recentDates) {
			const filePath = path.join(TEST_DIR, `daemon-${dateStr}.log`);
			fakeFs.files.set(filePath, {
				content: `log for ${dateStr}`,
				mtimeMs: new Date(`${dateStr}T12:00:00.000Z`).getTime(),
			});
		}

		logFile.rotate();

		// Old files should be gone
		for (const dateStr of oldDates) {
			const filePath = path.join(TEST_DIR, `daemon-${dateStr}.log`);
			assert.ok(!fakeFs.files.has(filePath), `Old file ${dateStr} should have been deleted`);
		}
		// Recent files should remain
		for (const dateStr of recentDates) {
			const filePath = path.join(TEST_DIR, `daemon-${dateStr}.log`);
			assert.ok(fakeFs.files.has(filePath), `Recent file ${dateStr} should be kept`);
		}
	});

	// B7 — rotate() with retentionDays=0 keeps all files
	it('B7: rotate() with retentionDays=0 keeps all files', () => {
		const now = makeDate('2026-05-08T12:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 0,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		const dates = ['2026-01-01', '2026-02-15', '2026-03-31', '2026-04-20', '2026-05-01'];
		for (const dateStr of dates) {
			const filePath = path.join(TEST_DIR, `daemon-${dateStr}.log`);
			fakeFs.files.set(filePath, {
				content: `log for ${dateStr}`,
				mtimeMs: new Date(`${dateStr}T00:00:00.000Z`).getTime(),
			});
		}

		logFile.rotate(); // retentionDays=0 → keep all

		for (const dateStr of dates) {
			const filePath = path.join(TEST_DIR, `daemon-${dateStr}.log`);
			assert.ok(fakeFs.files.has(filePath), `File ${dateStr} must not be deleted when retentionDays=0`);
		}
	});

	// B8 — dispose() closes stream and is idempotent
	it('B8: dispose() is idempotent and does not throw', () => {
		const now = makeDate('2026-05-08T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		// Open a stream by writing something
		logFile.writeStdout('open stream');

		// Double dispose must not throw
		assert.doesNotThrow(() => { logFile.dispose(); });
		assert.doesNotThrow(() => { logFile.dispose(); });

		// Writes after dispose must be silently ignored
		assert.doesNotThrow(() => { logFile.writeStdout('after dispose'); });
	});

	// B9 — mkdir EACCES is swallowed, writeStdout returns silently
	it('B9: mkdir EACCES is swallowed — no throw and no file written', () => {
		const now = makeDate('2026-05-08T10:00:00.000Z');
		const failingFs = createFakeFs({ mkdirFails: true });
		logFile = new DaemonLogFile({
			storageDir: '/restricted/path',
			retentionDays: 7,
			enabled: true,
			clock: () => now,
			fsImpl: failingFs,
		});

		// Must not throw despite mkdir failure
		assert.doesNotThrow(() => { logFile.writeStdout('should be swallowed'); });
		assert.doesNotThrow(() => { logFile.writeStderr('should be swallowed'); });
		assert.doesNotThrow(() => { logFile.writeEvent('should be swallowed'); });

		// No files should have been created
		assert.strictEqual(failingFs.files.size, 0, 'No files written when mkdir fails');
	});

	// B10 — non-matching filenames are NOT deleted by rotate()
	it('B10: rotate() does not delete non-matching filenames', () => {
		const now = makeDate('2026-05-08T12:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 1,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		// Files that do NOT match daemon-YYYY-MM-DD.log pattern — use path.join
		// (no extra normalize) to stay consistent with how unlinkSync keys are formed.
		const nonMatchingNames = ['other.txt', 'daemon-INVALID.log', 'daemon.log'];
		const nonMatchingPaths = nonMatchingNames.map((n) => path.join(TEST_DIR, n));

		// One old daemon log that SHOULD be deleted
		const oldDaemonLog = path.join(TEST_DIR, 'daemon-2026-01-01.log');

		for (const filePath of nonMatchingPaths) {
			fakeFs.files.set(filePath, { content: 'data', mtimeMs: 0 }); // mtime epoch — definitely old
		}
		fakeFs.files.set(oldDaemonLog, { content: 'old daemon log', mtimeMs: 0 });

		logFile.rotate();

		// Non-matching files must remain
		for (const filePath of nonMatchingPaths) {
			assert.ok(fakeFs.files.has(filePath), `Non-matching file must not be deleted: ${filePath}`);
		}

		// The old matching daemon log must be deleted
		assert.ok(!fakeFs.files.has(oldDaemonLog), 'Old daemon log matching pattern must be deleted');
	});

	// B12 — mkdir runs every rotation, not just first time (MEDIUM-2 fix)
	it('B12: mkdir is called on every daily rotation (recovers from external dir removal)', () => {
		let currentDate = makeDate('2026-03-01T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => currentDate,
			fsImpl: fakeFs,
		});

		// First write — creates dir + opens stream
		logFile.writeStdout('day 1 message');
		const mkdirAfterFirst = fakeFs.mkdirCallCount;
		assert.ok(mkdirAfterFirst >= 1, 'mkdir must be called on first write');

		// Advance to next day — triggers rotation
		currentDate = makeDate('2026-03-02T10:00:00.000Z');
		logFile.writeStdout('day 2 message');

		assert.ok(
			fakeFs.mkdirCallCount > mkdirAfterFirst,
			`mkdir must be called again on rotation — got ${fakeFs.mkdirCallCount} total calls, was ${mkdirAfterFirst} after first write`,
		);

		// Both days' files must exist
		assert.ok(fakeFs.files.has(path.join(TEST_DIR, 'daemon-2026-03-01.log')), 'day 1 file must exist');
		assert.ok(fakeFs.files.has(path.join(TEST_DIR, 'daemon-2026-03-02.log')), 'day 2 file must exist');
	});

	// B13 — mkdirSync re-creates directory after external deletion (MEDIUM-2 regression test)
	it('B13: re-creates storage directory if deleted externally between writes', () => {
		// Verifies the MEDIUM-2 fix: openStream() calls mkdirSync on every rotation,
		// so if the directory is removed externally between writes, the next write
		// (which triggers a rotation to a new date) re-creates the directory and
		// writes the new log file successfully.
		let currentDate = makeDate('2026-04-01T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => currentDate,
			fsImpl: fakeFs,
		});

		// First write: creates dir (mkdirSync called) and opens stream.
		logFile.writeStdout('first message');

		const day1File = path.join(TEST_DIR, 'daemon-2026-04-01.log');
		assert.ok(fakeFs.files.has(day1File), 'day 1 file must exist after first write');
		assert.ok(fakeFs.mkdirCalled, 'mkdirSync must have been called on first write');
		const mkdirCountAfterFirst = fakeFs.mkdirCallCount;

		// Simulate external directory deletion: remove all files that were in it.
		// (The fakeFs has no separate dir tracking; clearing the files and resetting
		// mkdirCallCount lets us verify the recovery path.)
		fakeFs.files.delete(day1File);

		// Advance to the next day — this triggers a rotation on the next write.
		currentDate = makeDate('2026-04-02T10:00:00.000Z');

		// Second write after external dir removal: openStream() must call mkdirSync
		// again (MEDIUM-2 fix — always mkdir on rotation), then write the new file.
		logFile.writeStdout('second message');

		const day2File = path.join(TEST_DIR, 'daemon-2026-04-02.log');
		assert.ok(
			fakeFs.mkdirCallCount > mkdirCountAfterFirst,
			`mkdirSync must be called again on rotation (total: ${fakeFs.mkdirCallCount}, was: ${mkdirCountAfterFirst})`,
		);
		assert.ok(fakeFs.files.has(day2File), 'day 2 file must exist — dir re-created after external deletion');

		const content = fakeFs.files.get(day2File)!.content;
		assert.ok(content.includes('second message'),
			`day 2 file must contain "second message", got: ${content}`);
	});

	// B11 — empty / whitespace-only writes are no-ops
	it('B11: empty or whitespace-only writes do not open a file', () => {
		const now = makeDate('2026-05-08T10:00:00.000Z');
		logFile = new DaemonLogFile({
			storageDir: TEST_DIR,
			retentionDays: 7,
			enabled: true,
			clock: () => now,
			fsImpl: fakeFs,
		});

		logFile.writeStdout('');
		logFile.writeStdout('   ');
		logFile.writeStdout('\n');
		logFile.writeStdout('\t  \n');
		logFile.writeStderr('');
		logFile.writeEvent('');

		assert.strictEqual(fakeFs.files.size, 0, 'Empty/whitespace writes must not create any files');
	});
});
