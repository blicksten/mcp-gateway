import { strict as assert } from 'node:assert';
import {
    createDefaultRespawnCoordinator,
    type ClaimRespawnApi,
} from '../respawn-coordinator';

interface FakeClient extends ClaimRespawnApi {
    calls: Array<{ started_at_ms: number; pid?: number; window_id?: string }>;
    response: { kind: 'won' | 'lost'; claimed_by?: { pid: number; window_id?: string; claimed_at_ms: number } };
    fail?: boolean;
}

function fakeClient(initial: Partial<FakeClient> = {}): FakeClient {
    const c: FakeClient = {
        calls: [],
        response: { kind: 'won' },
        ...initial,
        async claimRespawn(req) {
            c.calls.push(req);
            if (c.fail) {
                throw new Error('simulated transport failure');
            }
            return c.response;
        },
    };
    return c;
}

describe('respawn-coordinator (Option B — gateway-backed)', () => {
    it('returns won when gateway responds kind=won', async () => {
        const client = fakeClient({ response: { kind: 'won' } });
        const c = createDefaultRespawnCoordinator(client, { pid: 100, windowId: 'win-A' });

        const result = await c.claim(1779613481000);
        assert.equal(result.kind, 'won');
        assert.equal(client.calls.length, 1);
        assert.equal(client.calls[0].started_at_ms, 1779613481000);
        assert.equal(client.calls[0].pid, 100);
        assert.equal(client.calls[0].window_id, 'win-A');
    });

    it('returns lost with claimedBy metadata when gateway responds kind=lost', async () => {
        const client = fakeClient({
            response: {
                kind: 'lost',
                claimed_by: { pid: 999, window_id: 'win-winner', claimed_at_ms: 1779613500000 },
            },
        });
        const c = createDefaultRespawnCoordinator(client, { pid: 200 });

        const result = await c.claim(1779613481000);
        assert.equal(result.kind, 'lost');
        if (result.kind === 'lost') {
            assert.equal(result.claimedBy.pid, 999);
            assert.equal(result.claimedBy.windowId, 'win-winner');
            assert.equal(result.claimedBy.claimedAtMs, 1779613500000);
        }
    });

    it('treats malformed startedAtMs as won (over-prompt better than silent loss)', async () => {
        const client = fakeClient();
        const c = createDefaultRespawnCoordinator(client, { pid: 100 });

        const result = await c.claim(0);
        assert.equal(result.kind, 'won');
        assert.equal(client.calls.length, 0, 'invalid input must not reach the gateway');

        const negResult = await c.claim(-5);
        assert.equal(negResult.kind, 'won');
        assert.equal(client.calls.length, 0);

        const nanResult = await c.claim(Number.NaN);
        assert.equal(nanResult.kind, 'won');
        assert.equal(client.calls.length, 0);
    });

    it('falls back to won on transport / HTTP error', async () => {
        const client = fakeClient({ fail: true });
        const c = createDefaultRespawnCoordinator(client, { pid: 100 });

        const result = await c.claim(1779613481000);
        assert.equal(result.kind, 'won', 'transport failure must fall back to won, not silent loss');
        assert.equal(client.calls.length, 1, 'the failed call must still have been attempted');
    });

    it('treats kind=lost without claimedBy metadata as a normal lost', async () => {
        const client = fakeClient({ response: { kind: 'lost' } });
        const c = createDefaultRespawnCoordinator(client, { pid: 100 });

        const result = await c.claim(1779613481000);
        assert.equal(result.kind, 'lost', 'missing metadata should not flip to won');
        if (result.kind === 'lost') {
            assert.equal(result.claimedBy.pid, 0, 'placeholder pid surfaces the missing metadata');
        }
    });

    it('uses process.pid by default', async () => {
        const client = fakeClient();
        const c = createDefaultRespawnCoordinator(client);
        await c.claim(1779613481000);
        assert.equal(client.calls[0].pid, process.pid);
    });

    it('propagates explicit windowId when provided', async () => {
        const client = fakeClient();
        const c = createDefaultRespawnCoordinator(client, { windowId: 'win-xyz' });
        await c.claim(1779613481000);
        assert.equal(client.calls[0].window_id, 'win-xyz');
    });

    it('omits empty windowId by passing empty string (gateway treats as absent)', async () => {
        const client = fakeClient();
        const c = createDefaultRespawnCoordinator(client);
        await c.claim(1779613481000);
        assert.equal(client.calls[0].window_id, '');
    });
});
