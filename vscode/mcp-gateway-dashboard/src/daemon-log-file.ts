/**
 * DaemonLogFile — persistent file-backed log sink for daemon stdout/stderr.
 *
 * Appends timestamped lines to a daily rotating log file:
 *   <storageDir>/daemon-YYYY-MM-DD.log
 *
 * Design principles (mirrors logger.ts pattern):
 *   - Never throws — all errors are swallowed to protect callers.
 *   - Lazy mkdir — storageDir is created on first write, not in constructor.
 *   - Daily rotation — detects date change in append(); reopens file automatically.
 *   - Stream errors — attached .on('error',...) so EBADF/ENOSPC do not crash VSCode.
 */

import * as fs from 'node:fs';
import * as path from 'node:path';
import type * as vscode from 'vscode';

const FILENAME_PREFIX = 'daemon-';
const FILENAME_SUFFIX = '.log';
// "daemon-YYYY-MM-DD.log" — total length is 21 chars
const EXPECTED_LOG_FILENAME_LEN = 21;

// LOW-1: explicit char check per CLAUDE.md Regex Discipline — string operations
// used instead of regex because they are sufficient and avoid the regex-for-matching
// anti-pattern on a well-known fixed-length format.
/** Returns true if name matches the daemon-YYYY-MM-DD.log pattern. */
function isDaemonLogFilename(entry: string): boolean {
	if (entry.length !== EXPECTED_LOG_FILENAME_LEN) { return false; }
	if (!entry.startsWith(FILENAME_PREFIX)) { return false; }
	if (!entry.endsWith(FILENAME_SUFFIX)) { return false; }
	const datePart = entry.slice(FILENAME_PREFIX.length, entry.length - FILENAME_SUFFIX.length);
	// datePart must be "YYYY-MM-DD" — 10 chars with dashes at positions 4 and 7
	if (datePart.length !== 10 || datePart[4] !== '-' || datePart[7] !== '-') { return false; }
	// Digit checks: positions 0-3 (year), 5-6 (month), 8-9 (day)
	return allDigits(datePart, 0, 3) && allDigits(datePart, 5, 6) && allDigits(datePart, 8, 9);
}

function allDigits(s: string, lo: number, hi: number): boolean {
	for (let i = lo; i <= hi; i++) {
		const c = s.charCodeAt(i);
		if (c < 48 || c > 57) { return false; }
	}
	return true;
}

export interface DaemonLogFileOptions {
	/** Absolute path to the directory where daemon-YYYY-MM-DD.log files are written. */
	storageDir: string;
	/** Number of days to keep log files. 0 = keep forever. Default 7. */
	retentionDays: number;
	/** When false, all write calls are no-ops. Default true. */
	enabled: boolean;
	/** Injectable clock for tests. Defaults to () => new Date(). */
	clock?: () => Date;
	/** Injectable fs implementation for tests. Defaults to the real `fs`. */
	fsImpl?: typeof fs;
}

export class DaemonLogFile implements vscode.Disposable {
	private currentDateStamp = '';
	private writeStream: fs.WriteStream | undefined;
	private readonly storageDir: string;
	private readonly retentionDays: number;
	private readonly enabled: boolean;
	private readonly clock: () => Date;
	private readonly fs: typeof fs;
	private disposed = false;

	constructor(opts: DaemonLogFileOptions) {
		this.storageDir = opts.storageDir;
		this.retentionDays = opts.retentionDays;
		this.enabled = opts.enabled;
		this.clock = opts.clock ?? (() => new Date());
		this.fs = opts.fsImpl ?? fs;
	}

	writeStdout(text: string): void { this.append('stdout', text); }
	writeStderr(text: string): void { this.append('stderr', text); }
	writeEvent(text: string): void { this.append('event', text); }

	/**
	 * Delete daemon-YYYY-MM-DD.log files older than retentionDays in storageDir.
	 * Best-effort — swallows all errors.
	 */
	rotate(): void {
		if (!this.enabled || this.retentionDays === 0) { return; }
		try {
			const entries = this.fs.readdirSync(this.storageDir);
			const cutoff = Date.now() - this.retentionDays * 24 * 60 * 60 * 1000;
			for (const entry of entries) {
				if (!isDaemonLogFilename(entry)) { continue; }
				const filePath = path.join(this.storageDir, entry);
				try {
					const stat = this.fs.statSync(filePath);
					if (stat.mtimeMs < cutoff) {
						this.fs.unlinkSync(filePath);
					}
				} catch {
					// Best-effort — skip files we cannot stat or delete.
				}
			}
		} catch {
			// storageDir may not exist yet — that is fine.
		}
	}

	dispose(): void {
		if (this.disposed) { return; }
		this.disposed = true;
		if (this.writeStream) {
			try { this.writeStream.end(); } catch { /* swallow */ }
			this.writeStream = undefined;
		}
	}

	private append(stream: 'stdout' | 'stderr' | 'event', text: string): void {
		if (this.disposed || !this.enabled || !text.trim()) { return; }

		const now = this.clock();
		const dateStamp = this.formatDateStamp(now);

		if (dateStamp !== this.currentDateStamp) {
			this.closeStream();
			this.currentDateStamp = dateStamp;
			this.openStream(dateStamp);
		}

		if (!this.writeStream) { return; }

		const line = `[${now.toISOString()}] [${stream}] ${text.trimEnd()}\n`;
		try {
			this.writeStream.write(line);
		} catch {
			// Stream may be in a bad state — swallow to protect callers.
		}
	}

	private formatDateStamp(date: Date): string {
		const y = date.getFullYear();
		const m = String(date.getMonth() + 1).padStart(2, '0');
		const d = String(date.getDate()).padStart(2, '0');
		return `${y}-${m}-${d}`;
	}

	private openStream(dateStamp: string): void {
		// Always call mkdirSync on every rotation — it is a no-op when the dir
		// already exists (~microsecond cost) but recovers from external dir removal
		// between rotations (avoids silent log loss when storageDirCreated flag
		// would have short-circuited the call — MEDIUM-2 fix).
		try {
			this.fs.mkdirSync(this.storageDir, { recursive: true });
		} catch {
			// If mkdir fails, we cannot log — bail out silently.
			return;
		}
		const filePath = path.join(this.storageDir, `daemon-${dateStamp}.log`);
		try {
			this.writeStream = this.fs.createWriteStream(filePath, { flags: 'a', mode: 0o644 });
			this.writeStream.on('error', (err) => {
				console.error('[DaemonLogFile] write stream error:', err);
				this.writeStream = undefined;
			});
		} catch (err) {
			console.error('[DaemonLogFile] failed to open stream:', err);
			this.writeStream = undefined;
		}
	}

	private closeStream(): void {
		if (this.writeStream) {
			try { this.writeStream.end(); } catch { /* swallow */ }
			this.writeStream = undefined;
		}
	}
}
