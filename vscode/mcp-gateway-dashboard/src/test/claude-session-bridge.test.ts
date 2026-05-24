import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import {
    ClaudeSessionBridge,
    readClaudeSessions,
    type ClaudeSession,
    type RegisterPidApi,
} from '../claude-session-bridge';

function mkTempHome(): string {
    return fs.mkdtempSync(path.join(os.tmpdir(), 'claude-session-bridge-test-'));
}

function writeSession(home: string, name: string, body: object | string): string {
    const dir = path.join(home, 'sessions');
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, name);
    fs.writeFileSync(file, typeof body === 'string' ? body : JSON.stringify(body));
    return file;
}

function fakeClient(): RegisterPidApi & { calls: Array<{ session_id: string; pid: number; window_id?: string }>; fail?: boolean } {
    return {
        calls: [],
        fail: false,
        async registerPid(req) {
            this.calls.push(req);
            if (this.fail) {
                throw new Error('simulated transport failure');
            }
            return { stored: true };
        },
    } as RegisterPidApi & { calls: Array<{ session_id: string; pid: number; window_id?: string }>; fail?: boolean };
}

describe('claude-session-bridge', () => {
    describe('readClaudeSessions', () => {
        let home: string;

        beforeEach(() => {
            home = mkTempHome();
        });

        afterEach(() => {
            try { fs.rmSync(home, { recursive: true, force: true }); } catch { /* best-effort */ }
        });

        it('reads valid session files and extracts pid + sessionId', () => {
            writeSession(home, '12345.json', { pid: 12345, sessionId: 'abc-123', cwd: '/x', version: '2.1.145' });
            writeSession(home, '67890.json', { pid: 67890, sessionId: 'def-456', cwd: '/y' });

            const sessions = readClaudeSessions([home]);
            assert.equal(sessions.length, 2);
            const byPid = new Map(sessions.map(s => [s.pid, s] as const));
            assert.equal(byPid.get(12345)?.sessionId, 'abc-123');
            assert.equal(byPid.get(67890)?.sessionId, 'def-456');
        });

        it('silently skips malformed JSON files', () => {
            writeSession(home, '11111.json', { pid: 11111, sessionId: 'ok-1' });
            writeSession(home, '22222.json', 'not-valid-json{');
            writeSession(home, '33333.json', { pid: 33333, sessionId: 'ok-2' });

            const sessions = readClaudeSessions([home]);
            assert.equal(sessions.length, 2);
            assert.ok(sessions.find(s => s.sessionId === 'ok-1'));
            assert.ok(sessions.find(s => s.sessionId === 'ok-2'));
        });

        it('drops entries missing pid or sessionId', () => {
            writeSession(home, '1.json', { pid: 1, sessionId: 'has-both' });
            writeSession(home, '2.json', { pid: 2 }); // missing sessionId
            writeSession(home, '3.json', { sessionId: 'no-pid' }); // missing pid
            writeSession(home, '4.json', { pid: 'string-pid', sessionId: 'bad-pid-type' });
            writeSession(home, '5.json', { pid: 5, sessionId: '' }); // empty sessionId
            writeSession(home, '6.json', { pid: -7, sessionId: 'negative-pid' });
            writeSession(home, '7.json', { pid: 1.5, sessionId: 'float-pid' });

            const sessions = readClaudeSessions([home]);
            assert.equal(sessions.length, 1);
            assert.equal(sessions[0].sessionId, 'has-both');
        });

        it('returns empty array when home dir is missing', () => {
            const ghost = path.join(home, 'does-not-exist');
            const sessions = readClaudeSessions([ghost]);
            assert.deepEqual(sessions, []);
        });

        it('combines sessions from multiple home dirs', () => {
            const second = mkTempHome();
            try {
                writeSession(home, '100.json', { pid: 100, sessionId: 'home-1' });
                writeSession(second, '200.json', { pid: 200, sessionId: 'home-2' });

                const sessions = readClaudeSessions([home, second]);
                assert.equal(sessions.length, 2);
                assert.ok(sessions.find(s => s.sessionId === 'home-1'));
                assert.ok(sessions.find(s => s.sessionId === 'home-2'));
            } finally {
                try { fs.rmSync(second, { recursive: true, force: true }); } catch { /* best-effort */ }
            }
        });

        it('only reads .json files (skips other extensions)', () => {
            writeSession(home, 'ok.json', { pid: 1, sessionId: 'ok' });
            writeSession(home, 'note.txt', 'plain text');
            writeSession(home, 'config.yaml', 'yaml');

            const sessions = readClaudeSessions([home]);
            assert.equal(sessions.length, 1);
        });
    });

    describe('ClaudeSessionBridge.sync', () => {
        const fixedSessions = (s: ClaudeSession[]): (() => ClaudeSession[]) => () => s;

        it('posts register-pid once per unique (sessionId, pid) tuple', async () => {
            const client = fakeClient();
            const bridge = new ClaudeSessionBridge(
                client,
                fixedSessions([
                    { pid: 100, sessionId: 'sid-a', sourceFile: 'a.json' },
                    { pid: 200, sessionId: 'sid-b', sourceFile: 'b.json' },
                ]),
            );

            const posted = await bridge.sync('2026-05-24T09:00:00Z');
            assert.equal(posted, 2);
            assert.equal(client.calls.length, 2);
            assert.deepEqual(client.calls.map(c => c.session_id).sort(), ['sid-a', 'sid-b']);
        });

        it('skips already-registered tuples on second call with same gateway key', async () => {
            const client = fakeClient();
            const bridge = new ClaudeSessionBridge(
                client,
                fixedSessions([
                    { pid: 100, sessionId: 'sid-a', sourceFile: 'a.json' },
                ]),
            );

            await bridge.sync('2026-05-24T09:00:00Z');
            const posted2 = await bridge.sync('2026-05-24T09:00:00Z');
            assert.equal(posted2, 0);
            assert.equal(client.calls.length, 1);
        });

        it('re-registers everything when gateway key changes (respawn)', async () => {
            const client = fakeClient();
            const bridge = new ClaudeSessionBridge(
                client,
                fixedSessions([
                    { pid: 100, sessionId: 'sid-a', sourceFile: 'a.json' },
                    { pid: 200, sessionId: 'sid-b', sourceFile: 'b.json' },
                ]),
            );

            await bridge.sync('2026-05-24T09:00:00Z');
            const postedAfterRespawn = await bridge.sync('2026-05-24T11:00:00Z');
            assert.equal(postedAfterRespawn, 2);
            assert.equal(client.calls.length, 4);
        });

        it('returns 0 when gatewayKey is undefined (gateway unhealthy)', async () => {
            const client = fakeClient();
            const bridge = new ClaudeSessionBridge(
                client,
                fixedSessions([{ pid: 100, sessionId: 'sid-a', sourceFile: 'a.json' }]),
            );

            const posted = await bridge.sync(undefined);
            assert.equal(posted, 0);
            assert.equal(client.calls.length, 0);
        });

        it('swallows registerPid errors and continues with remaining sessions', async () => {
            const client = fakeClient();
            client.fail = true;
            const bridge = new ClaudeSessionBridge(
                client,
                fixedSessions([
                    { pid: 100, sessionId: 'sid-a', sourceFile: 'a.json' },
                    { pid: 200, sessionId: 'sid-b', sourceFile: 'b.json' },
                ]),
            );

            const posted = await bridge.sync('2026-05-24T09:00:00Z');
            assert.equal(posted, 0); // none counted as successfully posted
            assert.equal(client.calls.length, 2); // but both attempts fired
            // Neither tuple marked registered after failure → next sync retries.
            assert.equal(bridge.isRegistered('sid-a', 100), false);
        });

        it('does not call registerPid after dispose', async () => {
            const client = fakeClient();
            const bridge = new ClaudeSessionBridge(
                client,
                fixedSessions([{ pid: 100, sessionId: 'sid-a', sourceFile: 'a.json' }]),
            );
            bridge.dispose();
            const posted = await bridge.sync('2026-05-24T09:00:00Z');
            assert.equal(posted, 0);
            assert.equal(client.calls.length, 0);
        });

        it('propagates window_id to registerPid when provided', async () => {
            const client = fakeClient();
            const bridge = new ClaudeSessionBridge(
                client,
                fixedSessions([{ pid: 100, sessionId: 'sid-a', sourceFile: 'a.json' }]),
                'window-xyz',
            );

            await bridge.sync('2026-05-24T09:00:00Z');
            assert.equal(client.calls[0].window_id, 'window-xyz');
        });
    });
});
