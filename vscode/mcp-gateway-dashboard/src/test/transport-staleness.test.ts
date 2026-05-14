/**
 * Tests for transport-staleness detector (spike 2026-05-11 FM 3 D).
 *
 * Covers:
 *   - First health observation seeds last-seen, returns 0 (no respawn yet).
 *   - Same started_at on subsequent observations is a no-op.
 *   - started_at advancing (= respawn) with no stale siblings is a no-op.
 *   - Respawn + stale siblings + auto-kill off + user accepts -> kill called.
 *   - Respawn + stale siblings + auto-kill off + user dismisses -> no kill.
 *   - Respawn + stale siblings + auto-kill on -> silent kill, no prompt.
 *   - PowerShell JSON parser handles both single-object and array outputs.
 *   - filterStaleSiblings strict-less-than boundary.
 */

import './mock-vscode'; // must be imported first to intercept 'vscode' require
import * as assert from 'assert';
import {
    type SiblingClaudeProcess,
    type SiblingDetector,
    filterStaleSiblings,
    parsePowershellProcessJson,
    parseDotNetDate,
} from '../sibling-detector';
import {
    createDefaultStalenessDetector,
    buildPromptMessage,
} from '../transport-staleness';
import type { HealthResponse } from '../types';

function mockHealth(startedAt: string): HealthResponse {
    return {
        status: 'ok',
        servers: 1,
        running: 1,
        started_at: startedAt,
    };
}

function fakeSibling(pid: number, createdAtMs: number, parentPid = 6688): SiblingClaudeProcess {
    return {
        pid,
        parentPid,
        createdAt: new Date(createdAtMs),
        commandLine: `claude.exe --pid ${pid}`,
    };
}

class CannedSiblingDetector implements SiblingDetector {
    private readonly siblings: SiblingClaudeProcess[];
    constructor(siblings: SiblingClaudeProcess[]) { this.siblings = siblings; }
    async enumerate(): Promise<SiblingClaudeProcess[]> {
        return [...this.siblings];
    }
}

// --- Pure helper tests ---

describe('FM 3 D: sibling-detector helpers', () => {
    it('parseDotNetDate handles /Date(ms)/ format', () => {
        const d = parseDotNetDate('/Date(1715472000000)/');
        assert.ok(d !== null);
        assert.strictEqual(d!.getTime(), 1715472000000);
    });

    it('parseDotNetDate handles /Date(ms+offset)/ format', () => {
        const d = parseDotNetDate('/Date(1715472000000+0200)/');
        assert.ok(d !== null);
        // Offset suffix is informational only — UTC instant is the ms prefix.
        assert.strictEqual(d!.getTime(), 1715472000000);
    });

    it('parseDotNetDate rejects malformed input', () => {
        assert.strictEqual(parseDotNetDate('not a date'), null);
        assert.strictEqual(parseDotNetDate('/Date(NaN)/'), null);
        assert.strictEqual(parseDotNetDate(undefined), null);
        assert.strictEqual(parseDotNetDate(null), null);
    });

    it('parsePowershellProcessJson handles single-row object', () => {
        const json = JSON.stringify({
            ProcessId: 100,
            ParentProcessId: 6688,
            CreationDate: '/Date(1715472000000)/',
            CommandLine: 'claude.exe',
        });
        const out = parsePowershellProcessJson(json);
        assert.strictEqual(out.length, 1);
        assert.strictEqual(out[0].pid, 100);
        assert.strictEqual(out[0].parentPid, 6688);
    });

    it('parsePowershellProcessJson handles array output', () => {
        const json = JSON.stringify([
            { ProcessId: 1, ParentProcessId: 6688, CreationDate: '/Date(100)/', CommandLine: 'a' },
            { ProcessId: 2, ParentProcessId: 6688, CreationDate: '/Date(200)/', CommandLine: 'b' },
        ]);
        const out = parsePowershellProcessJson(json);
        assert.strictEqual(out.length, 2);
        assert.deepStrictEqual(out.map((s) => s.pid), [1, 2]);
    });

    it('parsePowershellProcessJson drops rows missing required fields', () => {
        const json = JSON.stringify([
            { ProcessId: 1, ParentProcessId: 6688, CreationDate: '/Date(100)/', CommandLine: 'ok' },
            { ProcessId: -1, ParentProcessId: 6688, CreationDate: '/Date(100)/' }, // bad pid
            { ProcessId: 3, ParentProcessId: 6688, CreationDate: 'garbage' }, // bad date
            null, // null row
        ]);
        const out = parsePowershellProcessJson(json);
        assert.strictEqual(out.length, 1);
        assert.strictEqual(out[0].pid, 1);
    });

    it('filterStaleSiblings is strictly less than gateway start', () => {
        const cutoff = new Date(1000);
        const siblings = [
            fakeSibling(1, 500), // older — stale
            fakeSibling(2, 1000), // equal — fresh
            fakeSibling(3, 1500), // newer — fresh
        ];
        const stale = filterStaleSiblings(siblings, cutoff);
        assert.deepStrictEqual(stale.map((s) => s.pid), [1]);
    });
});

// --- Detector behavior tests ---

describe('FM 3 D: staleness detector behavior', () => {
    it('first health observation seeds last-seen, returns 0', async () => {
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([fakeSibling(1, 0)]),
            killFn: () => { throw new Error('kill must NOT be called on first observation'); },
            autoKillEnabled: () => false,
            promptUser: async () => { throw new Error('prompt must NOT be called'); },
        });
        const n = await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        assert.strictEqual(n, 0);
    });

    it('same started_at twice is a no-op', async () => {
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([fakeSibling(1, 0)]),
            killFn: () => { throw new Error('kill must NOT be called'); },
            autoKillEnabled: () => false,
            promptUser: async () => { throw new Error('prompt must NOT be called'); },
        });
        await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        const n = await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        assert.strictEqual(n, 0);
    });

    it('respawn with no pre-existing siblings does not prompt', async () => {
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([]),
            killFn: () => { throw new Error('kill must NOT be called'); },
            autoKillEnabled: () => false,
            promptUser: async () => { throw new Error('prompt must NOT be called'); },
        });
        await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        const n = await detector.noteHealth(mockHealth('2026-05-11T06:00:00Z'));
        assert.strictEqual(n, 0);
    });

    it('respawn + opt-in auto-kill -> silent kill, no prompt', async () => {
        const killed: number[] = [];
        // Sibling created BEFORE the gateway respawn.
        const cutoffIso = '2026-05-11T05:00:00Z';
        const respawnIso = '2026-05-11T06:00:00Z';
        const siblings = [fakeSibling(101, Date.parse(cutoffIso) - 60_000)]; // 1 min older
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector(siblings),
            killFn: (pid) => { killed.push(pid); },
            autoKillEnabled: () => true,
            promptUser: async () => { throw new Error('prompt must NOT fire when autoKill ON'); },
        });
        await detector.noteHealth(mockHealth(cutoffIso));
        const n = await detector.noteHealth(mockHealth(respawnIso));
        assert.strictEqual(n, 1);
        assert.deepStrictEqual(killed, [101]);
    });

    it('respawn + user dismisses -> no kill', async () => {
        let promptShown = false;
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([fakeSibling(101, 0)]),
            killFn: () => { throw new Error('kill must NOT be called when user dismisses'); },
            autoKillEnabled: () => false,
            promptUser: async () => { promptShown = true; return 'Dismiss'; },
        });
        await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        const n = await detector.noteHealth(mockHealth('2026-05-11T06:00:00Z'));
        assert.strictEqual(n, 1, 'detector should still report the count of detected siblings');
        assert.strictEqual(promptShown, true);
    });

    it('respawn + user accepts -> kill called', async () => {
        const killed: number[] = [];
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([
                fakeSibling(101, 0),
                fakeSibling(102, 0),
            ]),
            killFn: (pid) => { killed.push(pid); },
            autoKillEnabled: () => false,
            promptUser: async () => 'Kill stale claude.exe sessions',
        });
        await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        const n = await detector.noteHealth(mockHealth('2026-05-11T06:00:00Z'));
        assert.strictEqual(n, 2);
        assert.deepStrictEqual(killed.sort(), [101, 102]);
    });

    it('repeated refreshes for same respawn cycle do not re-prompt for handled PIDs', async () => {
        let promptCount = 0;
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([fakeSibling(101, 0)]),
            killFn: () => { /* swallow */ },
            autoKillEnabled: () => false,
            promptUser: async () => {
                promptCount++;
                return 'Dismiss';
            },
        });
        await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        // First respawn — prompt fires once.
        await detector.noteHealth(mockHealth('2026-05-11T06:00:00Z'));
        // Subsequent refresh at the SAME started_at — no respawn → no prompt.
        await detector.noteHealth(mockHealth('2026-05-11T06:00:00Z'));
        await detector.noteHealth(mockHealth('2026-05-11T06:00:00Z'));
        assert.strictEqual(promptCount, 1);
    });

    it('respawn ignores siblings created AFTER the new gateway start', async () => {
        let killCalled = false;
        const respawnIso = '2026-05-11T06:00:00Z';
        const respawnMs = Date.parse(respawnIso);
        // Sibling created after respawn — fresh, not a candidate.
        const fresh = [fakeSibling(200, respawnMs + 1000)];
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector(fresh),
            killFn: () => { killCalled = true; },
            autoKillEnabled: () => true, // would silently kill if it matched
            promptUser: async () => 'Kill stale claude.exe sessions',
        });
        await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        const n = await detector.noteHealth(mockHealth(respawnIso));
        assert.strictEqual(n, 0);
        assert.strictEqual(killCalled, false);
    });

    it('null health observation is a no-op', async () => {
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([]),
            killFn: () => { throw new Error('kill must NOT be called'); },
            autoKillEnabled: () => false,
            promptUser: async () => { throw new Error('prompt must NOT be called'); },
        });
        const n = await detector.noteHealth(null);
        assert.strictEqual(n, 0);
    });

    it('dispose stops the detector — subsequent noteHealth returns 0', async () => {
        const detector = createDefaultStalenessDetector({
            siblingDetector: new CannedSiblingDetector([fakeSibling(101, 0)]),
            killFn: () => { throw new Error('kill must NOT be called after dispose'); },
            autoKillEnabled: () => true,
            promptUser: async () => 'Kill stale claude.exe sessions',
        });
        await detector.noteHealth(mockHealth('2026-05-11T05:00:00Z'));
        detector.dispose();
        const n = await detector.noteHealth(mockHealth('2026-05-11T06:00:00Z'));
        assert.strictEqual(n, 0);
    });

    it('buildPromptMessage uses singular for 1 candidate, plural otherwise', () => {
        const one = buildPromptMessage([fakeSibling(1, 0)]);
        assert.ok(one.includes('1 claude.exe session'));
        const many = buildPromptMessage([fakeSibling(1, 0), fakeSibling(2, 0)]);
        assert.ok(many.includes('2 claude.exe sessions'));
    });
});
