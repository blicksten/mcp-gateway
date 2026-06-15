// Settings webview — schema (Phase C T-C.1).
//
// Pure module — defines the form-layout view of mcpGateway.* settings used
// by `webview/settings-html.ts` and `webview/settings-panel.ts`. The schema
// shape is intentionally simple (sections + fields) so the webview can
// render without re-querying VS Code's `package.json` contributions at
// runtime.
//
// The keys here MUST stay aligned with `package.json` →
// `contributes.configuration.properties` — `npm run lint` (tsc) catches
// type drift; the test suite enforces presence of the 4 new Phase C keys
// (defaultVspCommand / defaultGuiUvProject / defaultGuiMode / uvPath).

export type FieldType = 'boolean' | 'number' | 'string' | 'enum';
/** `kind: 'path'` is a hint for the webview to render a Browse button +
 *  fire live validation against the value. Type stays `string`. */
export type FieldKind = 'plain' | 'path';

export interface SettingsField {
	key: string;            // mcpGateway.* dotted path
	label: string;
	description: string;
	type: FieldType;
	kind: FieldKind;
	choices?: string[];     // enum-only
	min?: number;           // number-only
	restartRequired: boolean;
}

export interface SettingsSection {
	title: string;
	fields: SettingsField[];
}

export interface SettingsSchema {
	sections: SettingsSection[];
}

/** Staged-or-current value map — keyed by dotted setting path. */
export type SettingsValues = Record<string, unknown>;

/** Restart-required keys: changing any of these triggers the toast that
 *  prompts the operator to restart the daemon (R-29 / X5 fix). */
export const RESTART_REQUIRED_KEYS: readonly string[] = [
	'mcpGateway.apiUrl',
	'mcpGateway.daemonPath',
	'mcpGateway.authTokenPath',
	'mcpGateway.claudeConfigSync.enabled',
	'mcpGateway.claudeConfigSync.path',
	'mcpGateway.claudeConfigSync.namespacePrefix',
	'mcpGateway.claudeConfigSync.aggregateEntryName',
	'mcpGateway.claudeConfigSync.reflectPerBackend',
];

/** mcpDashboard.* → mcpGateway.* mapping for the Import button (S1 fix).
 *  Each entry: source key in `mcpDashboard.*` → target key in `mcpGateway.*`.
 *  The optional `extra` block is applied alongside the primary mapping
 *  (e.g. `sapGuiPath` → `defaultGuiUvProject` AND set `defaultGuiMode='uv'`). */
export interface ImportMapping {
	source: string;
	target: string;
	extra?: Record<string, unknown>;
}

export const MCP_DASHBOARD_IMPORT_MAPPINGS: readonly ImportMapping[] = [
	{ source: 'mcpDashboard.keepassDbPath', target: 'mcpGateway.keepassPath' },
	{ source: 'mcpDashboard.vibingPath', target: 'mcpGateway.defaultVspCommand' },
	{
		source: 'mcpDashboard.sapGuiPath', target: 'mcpGateway.defaultGuiUvProject',
		extra: { 'mcpGateway.defaultGuiMode': 'uv' },
	},
	{ source: 'mcpDashboard.uvPath', target: 'mcpGateway.uvPath' },
];

/** The canonical schema shown by the settings webview. Keys mirror
 *  `package.json` `contributes.configuration.properties`. */
export const SETTINGS_SCHEMA: SettingsSchema = {
	sections: [
		{
			title: 'Connection',
			fields: [
				{
					key: 'mcpGateway.apiUrl', label: 'API URL', type: 'string', kind: 'plain',
					description: 'MCP Gateway REST API URL (default http://localhost:8765).',
					restartRequired: true,
				},
				{
					key: 'mcpGateway.pollInterval', label: 'Poll interval (ms)', type: 'number', kind: 'plain',
					description: 'Status polling interval in milliseconds (minimum 1000).',
					min: 1000, restartRequired: false,
				},
				{
					key: 'mcpGateway.verboseLogging', label: 'Verbose REST logging', type: 'boolean', kind: 'plain',
					description: 'Log every gateway REST call/response to OutputChannel "MCP Gateway".',
					restartRequired: false,
				},
			],
		},
		{
			title: 'Daemon',
			fields: [
				{
					key: 'mcpGateway.autoStart', label: 'Auto-start daemon', type: 'boolean', kind: 'plain',
					description: 'Start the gateway daemon automatically when VS Code opens.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.daemonPath', label: 'Daemon path', type: 'string', kind: 'path',
					description: 'Path to mcp-gateway executable (empty = use PATH).',
					restartRequired: true,
				},
				{
					key: 'mcpGateway.autoRestartOnCrash', label: 'Auto-restart on crash', type: 'boolean', kind: 'plain',
					description: 'Respawn the daemon on unexpected exit. Exponential backoff 1s→60s, max 5 quick crashes per 10 min.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.daemonFileLogEnabled', label: 'Persist daemon logs', type: 'boolean', kind: 'plain',
					description: 'Write daemon stdout/stderr to a daily log under globalStorage.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.daemonLogRetentionDays', label: 'Log retention (days)', type: 'number', kind: 'plain',
					description: 'Days to keep daemon-YYYY-MM-DD.log files (0 = forever).',
					min: 0, restartRequired: false,
				},
			],
		},
		{
			title: 'Auth',
			fields: [
				{
					key: 'mcpGateway.authTokenPath', label: 'Auth token path', type: 'string', kind: 'path',
					description: 'Bearer auth token file (default ~/.mcp-gateway/auth.token). MCP_GATEWAY_AUTH_TOKEN env overrides.',
					restartRequired: true,
				},
			],
		},
		{
			title: 'KeePass',
			fields: [
				{
					key: 'mcpGateway.keepassEnabled', label: 'Show KeePass-imported SAPs', type: 'boolean', kind: 'plain',
					description: 'Show union of daemon-detected and KeePass-imported SAP systems in the SAP tree.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.keepassPath', label: 'KDBX file', type: 'string', kind: 'path',
					description: 'Path to the KDBX file used by the credential import command.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.keepassGroup', label: 'KeePass group', type: 'string', kind: 'plain',
					description: 'Optional group filter (e.g. "MCP/Servers"). Empty = all groups.',
					restartRequired: false,
				},
			],
		},
		{
			title: 'SAP',
			fields: [
				{
					key: 'mcpGateway.sapSystemsEnabled', label: 'Show SAP Systems view', type: 'boolean', kind: 'plain',
					description: 'Reveal the SAP Systems view in the activity bar. Requires Reload Window.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.sapGroupBySid', label: 'Group SAPs by SID', type: 'boolean', kind: 'plain',
					description: 'Hierarchical SID → VSP/GUI rows. Off = composite-status flat rows.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.defaultVspCommand', label: 'Default VSP command', type: 'string', kind: 'path',
					description: 'Default VSP executable used by SAP Picker / quick-add when none is set per row.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.defaultGuiUvProject', label: 'Default GUI uv-project', type: 'string', kind: 'path',
					description: 'Default uv project path for the SAP-GUI server (used when defaultGuiMode = uv).',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.defaultGuiMode', label: 'Default GUI mode', type: 'enum', kind: 'plain',
					choices: ['exec', 'uv'],
					description: 'How the SAP-GUI server is launched: direct exec, or via uv-run from the project directory above.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.uvPath', label: 'uv executable', type: 'string', kind: 'path',
					description: 'Path to the uv binary (empty = use PATH).',
					restartRequired: false,
				},
			],
		},
		{
			title: 'Catalog & Slash Commands',
			fields: [
				{
					key: 'mcpGateway.catalogPath', label: 'Catalog directory', type: 'string', kind: 'path',
					description: 'Override directory for servers.json / commands.json (empty = bundled).',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.marketplaceJsonPath', label: 'marketplace.json override', type: 'string', kind: 'path',
					description: 'Override path for marketplace.json used by the Activate button. Empty = ancestor walk.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.mcpCtlPath', label: 'mcp-ctl executable', type: 'string', kind: 'path',
					description: 'Path to the mcp-ctl binary (empty = use PATH).',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.slashCommandsEnabled', label: 'Auto-generate slash commands', type: 'boolean', kind: 'plain',
					description: 'Write .claude/commands/<server>.md files when servers start/stop.',
					restartRequired: false,
				},
				{
					key: 'mcpGateway.slashCommandsPath', label: 'Slash commands directory', type: 'string', kind: 'plain',
					description: 'Output directory for slash command files. Supports ${workspaceFolder}.',
					restartRequired: false,
				},
			],
		},
		{
			title: 'Claude Code Sync',
			fields: [
				{
					key: 'mcpGateway.claudeConfigSync.enabled', label: 'Enable reflector', type: 'boolean', kind: 'plain',
					description: 'Reflect gateway backends into ~/.claude.json::mcpServers (workaround for v2.1.123+).',
					restartRequired: true,
				},
				{
					key: 'mcpGateway.claudeConfigSync.namespacePrefix', label: 'Namespace prefix', type: 'string', kind: 'plain',
					description: 'Prefix for managed entries. Entries with this prefix are owned by the extension.',
					restartRequired: true,
				},
				{
					key: 'mcpGateway.claudeConfigSync.path', label: 'Config path override', type: 'string', kind: 'path',
					description: 'Override path to ~/.claude.json (empty = default).',
					restartRequired: true,
				},
				{
					key: 'mcpGateway.claudeConfigSync.aggregateEntryName', label: 'Aggregate entry name', type: 'string', kind: 'plain',
					description: 'Name of the aggregate gateway entry (empty disables it).',
					restartRequired: true,
				},
				{
					key: 'mcpGateway.claudeConfigSync.reflectPerBackend', label: 'Reflect per-backend entries', type: 'boolean', kind: 'plain',
					description: 'OFF by default. Per-backend entries cause the ~/.claude.json write-war and the 60000ms MCP init timeout. The aggregate entry already exposes all backend tools.',
					restartRequired: true,
				},
			],
		},
	],
};

/** Flatten the schema to a key-indexed map of fields. */
export function indexSchema(schema: SettingsSchema): Map<string, SettingsField> {
	const idx = new Map<string, SettingsField>();
	for (const sec of schema.sections) {
		for (const f of sec.fields) { idx.set(f.key, f); }
	}
	return idx;
}

/** Decide the apply summary. Returns the list of restart-required keys
 *  among the changed set so the panel can render the toast. */
export function changedRestartRequiredKeys(changedKeys: string[]): string[] {
	const set = new Set<string>(RESTART_REQUIRED_KEYS);
	return changedKeys.filter((k) => set.has(k));
}
