import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { createDefaultRespawnCoordinator } from '../respawn-coordinator';

function mkTempBase(): string {
    return fs.mkdtempSync(path.join(os.tmpdir(), 'respawn-coordinator-test-'));
}

describe('respawn-coordinator', () => {
    let baseDir: string;

    beforeEach(() => {
        baseDir = mkTempBase();
    });

    afterEach(() => {
        try { fs.rmSync(baseDir, { recursive: true, force: true }); } catch { /* best-effort */ }
    });

    describe('claim', () => {
        it('first claim wins and writes the sentinel file', () => {
            const c = createDefaultRespawnCoordinator({ baseDir, pid: 100, windowId: 'win-A' });
            const result = c.claim(1779613481000);
            assert.equal(result.kind, 'won');

            const expected = path.join(baseDir, 'respawn-1779613481000.claimed');
            assert.ok(fs.existsSync(expected), 'sentinel file should be created');
            const payload = JSON.parse(fs.readFileSync(expected, 'utf8'));
            assert.equal(payload.pid, 100);
            assert.equal(payload.windowId, 'win-A');
            assert.equal(typeof payload.claimedAtMs, 'number');
        });

        it('second claim for the same started_at loses and reads the winner', () => {
            const winner = createDefaultRespawnCoordinator({ baseDir, pid: 100, windowId: 'win-A' });
            const loser = createDefaultRespawnCoordinator({ baseDir, pid: 200, windowId: 'win-B' });

            const winResult = winner.claim(1779613481000);
            const loseResult = loser.claim(1779613481000);

            assert.equal(winResult.kind, 'won');
            assert.equal(loseResult.kind, 'lost');
            if (loseResult.kind === 'lost') {
                assert.equal(loseResult.claimedBy.pid, 100);
                assert.equal(loseResult.claimedBy.windowId, 'win-A');
            }
        });

        it('different started_at values both win independently', () => {
            const c1 = createDefaultRespawnCoordinator({ baseDir, pid: 100 });
            const c2 = createDefaultRespawnCoordinator({ baseDir, pid: 200 });

            assert.equal(c1.claim(1779613000000).kind, 'won');
            assert.equal(c2.claim(1779613999000).kind, 'won');
        });

        it('treats sentinel-write failure (non-EEXIST) as won to avoid silent prompt loss', () => {
            // Point baseDir at a file path so mkdir + writeFile both fail in
            // a non-EEXIST way (path-component-is-file errors).
            const filePath = path.join(baseDir, 'a-file-not-a-dir');
            fs.writeFileSync(filePath, 'blocker');

            const c = createDefaultRespawnCoordinator({ baseDir: filePath, pid: 100 });
            const result = c.claim(1779613481000);
            // The non-EEXIST fallback path returns 'won' so the operator
            // still sees the prompt -- better than silent loss.
            assert.equal(result.kind, 'won');
        });
    });

    describe('sweepStale', () => {
        it('unlinks claim files older than 1h based on mtime', () => {
            const oldFile = path.join(baseDir, 'respawn-1.claimed');
            const newFile = path.join(baseDir, 'respawn-2.claimed');
            fs.writeFileSync(oldFile, '{}');
            fs.writeFileSync(newFile, '{}');
            // Backdate oldFile by 2h.
            const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000);
            fs.utimesSync(oldFile, twoHoursAgo, twoHoursAgo);

            const c = createDefaultRespawnCoordinator({ baseDir });
            const removed = c.sweepStale();
            assert.equal(removed, 1);
            assert.ok(!fs.existsSync(oldFile), 'stale file should be unlinked');
            assert.ok(fs.existsSync(newFile), 'fresh file should be kept');
        });

        it('ignores non-respawn files in the same directory', () => {
            const ours = path.join(baseDir, 'respawn-1.claimed');
            const theirs = path.join(baseDir, 'other-tool.lock');
            fs.writeFileSync(ours, '{}');
            fs.writeFileSync(theirs, '{}');
            const old = new Date(Date.now() - 2 * 60 * 60 * 1000);
            fs.utimesSync(ours, old, old);
            fs.utimesSync(theirs, old, old);

            const c = createDefaultRespawnCoordinator({ baseDir });
            c.sweepStale();
            assert.ok(!fs.existsSync(ours));
            assert.ok(fs.existsSync(theirs), 'foreign file should be untouched');
        });

        it('returns 0 when baseDir does not exist', () => {
            const ghost = path.join(baseDir, 'missing-dir');
            const c = createDefaultRespawnCoordinator({ baseDir: ghost });
            assert.equal(c.sweepStale(), 0);
        });

        it('claim() invokes sweep on each call', () => {
            const oldFile = path.join(baseDir, 'respawn-1.claimed');
            fs.writeFileSync(oldFile, '{}');
            fs.utimesSync(oldFile, new Date(Date.now() - 2 * 60 * 60 * 1000), new Date(Date.now() - 2 * 60 * 60 * 1000));

            const c = createDefaultRespawnCoordinator({ baseDir });
            c.claim(2);
            assert.ok(!fs.existsSync(oldFile), 'claim should sweep stale siblings');
        });
    });
});
