import './mock-vscode';
import { strict as assert } from 'node:assert';
import { describe, it, afterEach } from 'mocha';
import { SapPickerPanel } from '../webview/sap-picker-panel';
import * as importer from '../sap-picker-importer';
import { validateHttpsUrl, validateCookieFilePath } from '../validation';
import {
	rowKey,
	buildCloudVspArgs,
	type RowState,
	type PickerSnapshotRow,
	type CloudParams,
} from '../sap-picker-state';

// ---------------------------------------------------------------------------
// feature-a16d8b44 module 2 + module 3.
//
// SECURITY CONTRACT (asserted by these tests):
//   * Cloud rows inject EXACTLY SAP_URL / SAP_CLIENT / SAP_LANGUAGE.
//   * Cloud rows NEVER emit SAP_USER / SAP_PASSWORD / SAP_ENABLE_TRANSPORTS /
//     SAP_MODE, and NEVER call the KeePass credential fetcher.
//   * No password/cookie VALUE appears in any env entry — only structure/KEYS.
//
// enrichConfigWithCreds is private; we drive it via the prototype on a
// minimally-constructed instance (Object.create) so we exercise the real
// cloud branch without spawning a webview, daemon, or python process.
// ---------------------------------------------------------------------------

function snapRow(over: Partial<PickerSnapshotRow>): PickerSnapshotRow {
	return {
		sid: 'CLD',
		client: '100',
		user: '',
		kpMissing: false,
		registered: { vsp: false, gui: false },
		status: { vsp: '', gui: '' },
		...over,
	};
}

function cloudRow(over: Partial<RowState> & { snapshot: PickerSnapshotRow }): RowState {
	return {
		key: rowKey(over.snapshot),
		desired: { vsp: over.snapshot.registered.vsp, gui: over.snapshot.registered.gui },
		vspStatus: 'idle',
		guiStatus: 'idle',
		override: {},
		...over,
	};
}

const CLOUD: CloudParams = {
	sapUrl: 'https://my-tenant.s4hana.cloud',
	cookieFile: '/abs/path/cookies.txt',
	readOnly: true,
	featureRap: true,
};

/** Build a bare SapPickerPanel-shaped object exposing the private
 *  enrichConfigWithCreds with a chosen rows array. lastInputs +
 *  kpMasterPasswordBuf are deliberately POPULATED so that if the cloud branch
 *  ever fell through to the KeePass path it WOULD try to fetch — making the
 *  "fetch not called" assertion meaningful rather than vacuous. */
function panelWithRows(rows: RowState[]): {
	enrichConfigWithCreds: (op: { component: string; rowKey: string; serverName: string; config?: Record<string, unknown> }) => Promise<Record<string, unknown>>;
} {
	const inst = Object.create(SapPickerPanel.prototype);
	inst.rows = rows;
	inst.lastInputs = { kdbxPath: '/abs/db.kdbx', pythonPath: 'python', scriptPath: '/abs/sap-credentials.py' };
	inst.kpMasterPasswordBuf = Buffer.from('NOT-A-REAL-PASSWORD');
	return inst;
}

describe('cloud enrichConfigWithCreds (module 2)', () => {
	let fetchSpyCalls = 0;
	const realFetch = importer.fetchSapCredentials;

	afterEach(() => {
		// Restore the real export after any spy install.
		(importer as { fetchSapCredentials: unknown }).fetchSapCredentials = realFetch;
		fetchSpyCalls = 0;
	});

	function installFetchSpy(): void {
		fetchSpyCalls = 0;
		(importer as { fetchSapCredentials: unknown }).fetchSapCredentials = async () => {
			fetchSpyCalls++;
			throw new Error('fetchSapCredentials must NOT be called for cloud rows');
		};
	}

	it('cloud vsp row injects EXACTLY SAP_URL/SAP_CLIENT/SAP_LANGUAGE and no password keys', async () => {
		installFetchSpy();
		const rows = [cloudRow({ snapshot: snapRow({ sid: 'CLD', client: '100' }), kind: 'cloud', cloud: CLOUD })];
		const panel = panelWithRows(rows);
		const out = await panel.enrichConfigWithCreds({
			component: 'vsp',
			rowKey: 'CLD-100',
			serverName: 'vsp-CLD-100',
			config: { command: '/opt/vsp', args: buildCloudVspArgs(CLOUD) },
		});

		const env = out.env as string[];
		assert.ok(Array.isArray(env), 'env must be an array');

		// Exactly the three cloud env keys, nothing else.
		const keys = env.map((e) => e.split('=', 1)[0]).sort();
		assert.deepStrictEqual(keys, ['SAP_CLIENT', 'SAP_LANGUAGE', 'SAP_URL']);

		// Values are correct + URL is https.
		assert.ok(env.includes('SAP_URL=https://my-tenant.s4hana.cloud'));
		assert.ok(env.includes('SAP_CLIENT=100'));
		assert.ok(env.includes('SAP_LANGUAGE=EN'));

		// EXCLUDES password/transport/mode env (the on-prem-only keys).
		for (const banned of ['SAP_USER', 'SAP_PASSWORD', 'SAP_ENABLE_TRANSPORTS', 'SAP_MODE']) {
			assert.ok(!env.some((e) => e.startsWith(banned + '=')),
				`cloud env must not contain ${banned}`);
		}

		// command + args (cookie-file launcher) survive untouched.
		assert.strictEqual(out.command, '/opt/vsp');
		assert.deepStrictEqual(out.args,
			['--read-only', '--feature-rap', 'on', '--cookie-file', '/abs/path/cookies.txt']);

		// KeePass fetcher was never invoked.
		assert.strictEqual(fetchSpyCalls, 0, 'KeePass fetch must not be called for cloud rows');
	});

	it('cloud row honours a non-default lang override (SAP_LANGUAGE)', async () => {
		installFetchSpy();
		const rows = [cloudRow({
			snapshot: snapRow({ sid: 'CLD', client: '200' }),
			kind: 'cloud',
			cloud: { ...CLOUD, lang: 'DE' },
		})];
		const panel = panelWithRows(rows);
		const out = await panel.enrichConfigWithCreds({
			component: 'vsp', rowKey: 'CLD-200', serverName: 'vsp-CLD-200',
			config: { command: '/opt/vsp' },
		});
		const env = out.env as string[];
		assert.ok(env.includes('SAP_LANGUAGE=DE'));
		assert.ok(env.includes('SAP_CLIENT=200'));
		assert.strictEqual(fetchSpyCalls, 0);
	});

	it('no env entry leaks a secret value (cookie/password)', async () => {
		installFetchSpy();
		const rows = [cloudRow({ snapshot: snapRow({ sid: 'CLD', client: '100' }), kind: 'cloud', cloud: CLOUD })];
		const panel = panelWithRows(rows);
		const out = await panel.enrichConfigWithCreds({
			component: 'vsp', rowKey: 'CLD-100', serverName: 'vsp-CLD-100',
			config: { command: '/opt/vsp' },
		});
		const env = out.env as string[];
		for (const e of env) {
			assert.ok(!/PASSWORD|COOKIE|TOKEN/i.test(e.split('=', 1)[0]),
				`env key looks secret-bearing: ${e}`);
		}
	});
});

describe('validation: validateHttpsUrl (module 3)', () => {
	it('accepts https URLs', () => {
		assert.strictEqual(validateHttpsUrl('https://example.com'), true);
		assert.strictEqual(validateHttpsUrl('https://my-tenant.s4hana.cloud/path'), true);
		assert.strictEqual(validateHttpsUrl('  https://x  '), true);
	});

	it('rejects non-https and malformed inputs', () => {
		assert.strictEqual(validateHttpsUrl('http://example.com'), false);
		assert.strictEqual(validateHttpsUrl('ftp://example.com'), false);
		assert.strictEqual(validateHttpsUrl('example.com'), false);
		assert.strictEqual(validateHttpsUrl(''), false);
		assert.strictEqual(validateHttpsUrl('   '), false);
		// HTTPS embedded but not as scheme prefix.
		assert.strictEqual(validateHttpsUrl('x https://y'), false);
	});
});

describe('validation: validateCookieFilePath (module 3)', () => {
	it('accepts absolute paths (POSIX / Windows / UNC)', () => {
		assert.strictEqual(validateCookieFilePath('/abs/path/cookies.txt'), true);
		assert.strictEqual(validateCookieFilePath('C:\\cookies\\session.txt'), true);
		assert.strictEqual(validateCookieFilePath('C:/cookies/session.txt'), true);
		assert.strictEqual(validateCookieFilePath('\\\\host\\share\\c.txt'), true);
	});

	it('rejects relative and empty paths', () => {
		assert.strictEqual(validateCookieFilePath('cookies.txt'), false);
		assert.strictEqual(validateCookieFilePath('./cookies.txt'), false);
		assert.strictEqual(validateCookieFilePath('../cookies.txt'), false);
		assert.strictEqual(validateCookieFilePath(''), false);
		assert.strictEqual(validateCookieFilePath('   '), false);
	});
});
