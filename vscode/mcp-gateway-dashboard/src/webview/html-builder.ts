import type { ServerView, ToolInfo } from '../types';
import type { SapSystem } from '../sap-detector';

/** Escape HTML special characters to prevent XSS. */
export function escapeHtml(s: string): string {
	return s
		.replace(/&/g, '&amp;')
		.replace(/</g, '&lt;')
		.replace(/>/g, '&gt;')
		.replace(/"/g, '&quot;')
		.replace(/'/g, '&#39;');
}

/** Safely embed a value as JSON inside a <script> tag.
 *  Escapes < and > to Unicode escapes so HTML parser cannot be tricked. */
function jsonForScript(value: unknown): string {
	return JSON.stringify(value)
		.replace(/&/g, '\\u0026')
		.replace(/</g, '\\u003c')
		.replace(/>/g, '\\u003e');
}

interface McpDetailData {
	server: ServerView;
	credentialKeys: { env: string[]; headers: string[] };
	nonce: string;
	cspSource: string;
}

interface SapDetailData {
	system: SapSystem;
	vspCredentialKeys: { env: string[]; headers: string[] };
	guiCredentialKeys: { env: string[]; headers: string[] };
	nonce: string;
	cspSource: string;
}

/** Build MCP server detail webview HTML. */
export function buildMcpDetailHtml(data: McpDetailData): string {
	const { server, credentialKeys, nonce, cspSource } = data;
	const s = server;

	const toolRows = (s.tools ?? []).map((t: ToolInfo) =>
		`<tr><td>${escapeHtml(t.name)}</td><td>${escapeHtml(t.description)}</td></tr>`).join('');

	const envRows = credentialKeys.env.map((k) =>
		`<tr><td>${escapeHtml(k)}</td><td>********</td></tr>`).join('');

	const headerRows = credentialKeys.headers.map((k) =>
		`<tr><td>${escapeHtml(k)}</td><td>********</td></tr>`).join('');

	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${cspSource} 'nonce-${nonce}'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>${escapeHtml(s.name)}</title>
<style nonce="${nonce}">
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); background: var(--vscode-editor-background); padding: 12px; margin: 0; }
h1 { font-size: 1.3em; margin: 0 0 8px 0; }
h2 { font-size: 1.1em; margin: 16px 0 6px 0; border-bottom: 1px solid var(--vscode-panel-border); padding-bottom: 4px; }
table { border-collapse: collapse; width: 100%; margin: 6px 0; }
th, td { text-align: left; padding: 4px 8px; border-bottom: 1px solid var(--vscode-panel-border); }
th { font-weight: 600; }
.status { display: inline-block; padding: 2px 8px; border-radius: 3px; font-size: 0.9em; }
.status-running { background: var(--vscode-testing-iconPassed); color: var(--vscode-editor-background); }
.status-error { background: var(--vscode-testing-iconFailed); color: var(--vscode-editor-background); }
.status-stopped, .status-disabled { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); }
.status-degraded { background: var(--vscode-editorWarning-foreground); color: var(--vscode-editor-background); }
.actions { margin: 12px 0; display: flex; gap: 6px; flex-wrap: wrap; }
button { padding: 4px 12px; border: 1px solid var(--vscode-button-border, var(--vscode-panel-border)); background: var(--vscode-button-background); color: var(--vscode-button-foreground); cursor: pointer; border-radius: 3px; font-family: inherit; }
button:hover { background: var(--vscode-button-hoverBackground); }
.mono { font-family: var(--vscode-editor-font-family); font-size: var(--vscode-editor-font-size); }
.error-text { color: var(--vscode-errorForeground); }
</style>
</head>
<body>
<h1>${escapeHtml(s.name)} <span class="status status-${escapeHtml(s.status)}">${escapeHtml(s.status)}</span></h1>

<div class="actions" role="toolbar" aria-label="Server actions">
<button onclick="postAction('restart')" aria-label="Restart server">Restart</button>
<button onclick="postAction('${s.status === 'disabled' ? 'enable' : 'disable'}')" aria-label="${s.status === 'disabled' ? 'Enable' : 'Disable'} server">${s.status === 'disabled' ? 'Enable' : 'Disable'}</button>
<button onclick="postAction('resetCircuit')" aria-label="Reset circuit breaker">Reset Circuit</button>
<button onclick="postAction('showLogs')" aria-label="Show logs">Show Logs</button>
</div>

<h2>Configuration</h2>
<table>
<tr><th>Transport</th><td>${escapeHtml(s.transport || 'unknown')}</td></tr>
${s.pid != null ? `<tr><th>PID</th><td class="mono">${escapeHtml(String(s.pid))}</td></tr>` : ''}
<tr><th>Restart count</th><td>${escapeHtml(String(s.restart_count))}</td></tr>
${s.last_error ? `<tr><th>Last error</th><td class="error-text">${escapeHtml(s.last_error)}</td></tr>` : ''}
</table>

${(s.tools ?? []).length > 0 ? `
<h2>Tools (${(s.tools ?? []).length})</h2>
<table>
<tr><th>Name</th><th>Description</th></tr>
${toolRows}
</table>
` : '<h2>Tools</h2><p>No tools exposed.</p>'}

${(credentialKeys.env.length > 0 || credentialKeys.headers.length > 0) ? `
<h2>Credentials</h2>
${credentialKeys.env.length > 0 ? `
<h3 style="font-size:1em;margin:8px 0 4px;">Environment Variables</h3>
<table><tr><th>Key</th><th>Value</th></tr>${envRows}</table>` : ''}
${credentialKeys.headers.length > 0 ? `
<h3 style="font-size:1em;margin:8px 0 4px;">Headers</h3>
<table><tr><th>Name</th><th>Value</th></tr>${headerRows}</table>` : ''}
` : ''}

<script nonce="${nonce}">
const vscode = acquireVsCodeApi();
function postAction(action) {
	vscode.postMessage({ type: 'action', action: action, serverName: ${jsonForScript(s.name)} });
}
</script>
</body>
</html>`;
}

/** Build SAP system detail webview HTML. */
export function buildSapDetailHtml(data: SapDetailData): string {
	const { system, vspCredentialKeys, guiCredentialKeys, nonce, cspSource } = data;

	function componentSection(label: string, sv: ServerView | undefined, creds: { env: string[]; headers: string[] }): string {
		if (!sv) {
			return `<h2>${escapeHtml(label)}</h2><p>Not configured.</p>`;
		}
		const toolCount = sv.tools?.length ?? 0;
		const credRows = [
			...creds.env.map((k) => `<tr><td>${escapeHtml(k)}</td><td>env</td><td>********</td></tr>`),
			...creds.headers.map((k) => `<tr><td>${escapeHtml(k)}</td><td>header</td><td>********</td></tr>`),
		].join('');

		return `
<h2>${escapeHtml(label)}: ${escapeHtml(sv.name)} <span class="status status-${escapeHtml(sv.status)}">${escapeHtml(sv.status)}</span></h2>
<table>
<tr><th>Transport</th><td>${escapeHtml(sv.transport || 'unknown')}</td></tr>
${sv.pid != null ? `<tr><th>PID</th><td class="mono">${escapeHtml(String(sv.pid))}</td></tr>` : ''}
<tr><th>Tools</th><td>${escapeHtml(String(toolCount))}</td></tr>
<tr><th>Restart count</th><td>${escapeHtml(String(sv.restart_count))}</td></tr>
${sv.last_error ? `<tr><th>Last error</th><td class="error-text">${escapeHtml(sv.last_error)}</td></tr>` : ''}
</table>
${credRows ? `<table><tr><th>Key</th><th>Type</th><th>Value</th></tr>${credRows}</table>` : ''}`;
	}

	const title = system.client ? `${system.sid}-${system.client}` : system.sid;

	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${cspSource} 'nonce-${nonce}'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SAP ${escapeHtml(title)}</title>
<style nonce="${nonce}">
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); background: var(--vscode-editor-background); padding: 12px; margin: 0; }
h1 { font-size: 1.3em; margin: 0 0 8px 0; }
h2 { font-size: 1.1em; margin: 16px 0 6px 0; border-bottom: 1px solid var(--vscode-panel-border); padding-bottom: 4px; }
table { border-collapse: collapse; width: 100%; margin: 6px 0; }
th, td { text-align: left; padding: 4px 8px; border-bottom: 1px solid var(--vscode-panel-border); }
th { font-weight: 600; }
.status { display: inline-block; padding: 2px 8px; border-radius: 3px; font-size: 0.9em; }
.status-running { background: var(--vscode-testing-iconPassed); color: var(--vscode-editor-background); }
.status-error { background: var(--vscode-testing-iconFailed); color: var(--vscode-editor-background); }
.status-stopped, .status-disabled { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); }
.status-degraded { background: var(--vscode-editorWarning-foreground); color: var(--vscode-editor-background); }
.actions { margin: 12px 0; display: flex; gap: 6px; flex-wrap: wrap; }
button { padding: 4px 12px; border: 1px solid var(--vscode-button-border, var(--vscode-panel-border)); background: var(--vscode-button-background); color: var(--vscode-button-foreground); cursor: pointer; border-radius: 3px; font-family: inherit; }
button:hover { background: var(--vscode-button-hoverBackground); }
.mono { font-family: var(--vscode-editor-font-family); font-size: var(--vscode-editor-font-size); }
.error-text { color: var(--vscode-errorForeground); }
</style>
</head>
<body>
<h1>SAP ${escapeHtml(title)} <span class="status status-${escapeHtml(system.status)}">${escapeHtml(system.status)}</span></h1>

<div class="actions" role="toolbar" aria-label="SAP system actions">
${system.vsp ? `<button onclick="postAction('restart', 'vsp')" aria-label="Restart VSP">Restart VSP</button>` : ''}
${system.gui ? `<button onclick="postAction('restart', 'gui')" aria-label="Restart GUI">Restart GUI</button>` : ''}
${system.vsp ? `<button onclick="postAction('showLogs', 'vsp')" aria-label="Show VSP logs">VSP Logs</button>` : ''}
${system.gui ? `<button onclick="postAction('showLogs', 'gui')" aria-label="Show GUI logs">GUI Logs</button>` : ''}
</div>

${componentSection('VSP', system.vsp, vspCredentialKeys)}
${componentSection('GUI', system.gui, guiCredentialKeys)}

<script nonce="${nonce}">
const vscode = acquireVsCodeApi();
function postAction(action, component) {
	vscode.postMessage({ type: 'action', action: action, component: component,
		serverName: component === 'gui' ? ${jsonForScript(system.gui?.name ?? '')} : ${jsonForScript(system.vsp?.name ?? '')} });
}
</script>
</body>
</html>`;
}
