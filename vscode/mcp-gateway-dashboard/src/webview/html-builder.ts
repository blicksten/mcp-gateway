import type { ServerView, ToolInfo } from '../types';
import type { SapSystem } from '../sap-detector';
import { SERVER_NAME_RE, ENV_KEY_RE, HEADER_NAME_RE } from '../validation';

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

/** Build empty-state HTML for the sidebar detail view (no server selected). */
export function buildDetailPlaceholderHtml(nonce: string, cspSource: string): string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${cspSource} 'nonce-${nonce}'; script-src 'none';">
<title>Server Detail</title>
<style nonce="${nonce}">
body { font-family: var(--vscode-font-family); color: var(--vscode-descriptionForeground); background: var(--vscode-editor-background); padding: 12px; margin: 0; font-style: italic; }
</style>
</head>
<body>
<p>Select a server to view details.</p>
</body>
</html>`;
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

/** Build Add Server webview form HTML. Client-side validation + auto-detect transport. */
export function buildAddServerHtml(nonce: string, cspSource: string): string {
	// Ship the canonical regex patterns (source strings, not RegExp objects) from
	// validation.ts into the script so webview validators cannot drift from the
	// extension-side validators. JSON-encoded to avoid template-literal escaping
	// hazards (the HEADER_NAME_RE character class contains a backtick).
	const serverNameRe = jsonForScript(SERVER_NAME_RE.source);
	const envKeyRe = jsonForScript(ENV_KEY_RE.source);
	const headerNameRe = jsonForScript(HEADER_NAME_RE.source);

	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${cspSource} 'nonce-${nonce}'; script-src 'nonce-${nonce}'; form-action 'none';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Add Server</title>
<style nonce="${nonce}">
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); background: var(--vscode-editor-background); padding: 16px; margin: 0; }
h1 { font-size: 1.25em; margin: 0 0 12px 0; }
form { display: flex; flex-direction: column; gap: 12px; max-width: 640px; }
label { display: flex; flex-direction: column; gap: 4px; font-size: 0.95em; }
.hint { font-size: 0.85em; color: var(--vscode-descriptionForeground); }
input[type="text"], textarea { font-family: var(--vscode-editor-font-family); font-size: var(--vscode-editor-font-size); padding: 6px 8px; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); border-radius: 2px; }
textarea { min-height: 64px; resize: vertical; }
input:focus, textarea:focus { outline: 1px solid var(--vscode-focusBorder); }
.actions { display: flex; gap: 8px; margin-top: 4px; }
button { padding: 6px 16px; border: 1px solid var(--vscode-button-border, var(--vscode-panel-border)); background: var(--vscode-button-background); color: var(--vscode-button-foreground); cursor: pointer; border-radius: 2px; font-family: inherit; font-size: 0.95em; }
button:hover { background: var(--vscode-button-hoverBackground); }
button.secondary { background: var(--vscode-button-secondaryBackground); color: var(--vscode-button-secondaryForeground); }
button.secondary:hover { background: var(--vscode-button-secondaryHoverBackground); }
button:disabled { opacity: 0.5; cursor: default; }
.detected { display: inline-block; padding: 1px 8px; border-radius: 3px; font-size: 0.8em; background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); margin-left: 6px; }
.error { color: var(--vscode-errorForeground); font-size: 0.85em; min-height: 1em; }
.banner { padding: 8px 12px; border-radius: 3px; margin-bottom: 8px; font-size: 0.9em; display: none; }
.banner.err { background: var(--vscode-inputValidation-errorBackground); color: var(--vscode-inputValidation-errorForeground); border: 1px solid var(--vscode-inputValidation-errorBorder); }
</style>
</head>
<body>
<h1>Add MCP Server</h1>
<div id="banner" class="banner err" role="alert"></div>
<form id="addForm" novalidate>
  <label>
    Name
    <input type="text" id="name" name="name" autocomplete="off" spellcheck="false" placeholder="my-mcp-server" required>
    <span class="hint">Letters, digits, hyphens, underscores. Max 64 chars.</span>
    <span class="error" id="name-err"></span>
  </label>

  <label>
    URL or command <span id="detected" class="detected">stdio</span>
    <input type="text" id="target" name="target" autocomplete="off" spellcheck="false" placeholder="/usr/local/bin/my-mcp-server  or  http://localhost:3000/mcp" required>
    <span class="hint">Starts with http:// or https:// = HTTP transport; otherwise absolute path = stdio.</span>
    <span class="error" id="target-err"></span>
  </label>

  <label>
    Environment variables <span class="hint">(optional, one KEY=VALUE per line)</span>
    <textarea id="env" name="env" spellcheck="false" placeholder="API_KEY=sk-...&#10;DEBUG=1"></textarea>
    <span class="error" id="env-err"></span>
  </label>

  <label>
    Headers <span class="hint">(optional, one Name: Value per line — HTTP transport only)</span>
    <textarea id="headers" name="headers" spellcheck="false" placeholder="Authorization: Bearer token&#10;X-Custom: value"></textarea>
    <span class="error" id="headers-err"></span>
  </label>

  <div class="actions">
    <button type="submit" id="submit">Add server</button>
    <button type="button" id="cancel" class="secondary">Cancel</button>
  </div>
</form>

<script nonce="${nonce}">
const vscode = acquireVsCodeApi();

// Regex sources are injected from src/validation.ts so the webview cannot
// drift from the authoritative patterns used by the extension host.
const SERVER_NAME_RE = new RegExp(${serverNameRe});
const ENV_KEY_RE = new RegExp(${envKeyRe});
const HEADER_NAME_RE = new RegExp(${headerNameRe});

const $ = (id) => document.getElementById(id);

function detectTransport(v) {
  const t = v.trim();
  if (t.startsWith('http://') || t.startsWith('https://')) { return 'http'; }
  return 'stdio';
}

// Mirror of src/validation.ts#isAbsolutePath — string methods only, no regex.
// Recognizes POSIX absolute (leading /), UNC (\\\\host), and Windows drive
// letters (C:\\ or C:/). Kept in sync via tests in validation.test.ts and
// add-server-panel.test.ts.
function isAbsolutePath(p) {
  const s = p.trim();
  if (s.length === 0) { return false; }
  if (s.charAt(0) === '/') { return true; }
  if (s.length >= 2 && s.charAt(0) === '\\\\' && s.charAt(1) === '\\\\') { return true; }
  if (s.length >= 3) {
    const c = s.charCodeAt(0);
    const isLetter = (c >= 65 && c <= 90) || (c >= 97 && c <= 122);
    if (isLetter && s.charAt(1) === ':' && (s.charAt(2) === '\\\\' || s.charAt(2) === '/')) {
      return true;
    }
  }
  return false;
}

function validateName(v) {
  if (!v.trim()) { return 'Name is required'; }
  if (!SERVER_NAME_RE.test(v.trim())) { return 'Name must start with a letter/digit, max 64 chars, letters/digits/hyphens/underscores only'; }
  return null;
}

function validateTarget(v) {
  const t = v.trim();
  if (!t) { return 'URL or command is required'; }
  if (detectTransport(t) === 'http') {
    try {
      const parsed = new URL(t);
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') { return 'URL must use http: or https:'; }
    } catch { return 'Invalid URL format'; }
    return null;
  }
  if (!isAbsolutePath(t)) { return 'Use an absolute path for stdio commands'; }
  return null;
}

function validateEnv(v) {
  const lines = v.split(/\\r?\\n/).map(l => l.trim()).filter(l => l.length > 0);
  for (const line of lines) {
    const eq = line.indexOf('=');
    if (eq < 1) { return 'Each line must be KEY=VALUE: "' + line + '"'; }
    const key = line.substring(0, eq);
    if (!ENV_KEY_RE.test(key)) { return 'Invalid env var key: "' + key + '"'; }
  }
  return null;
}

function validateHeaders(v) {
  const lines = v.split(/\\r?\\n/).map(l => l.trim()).filter(l => l.length > 0);
  for (const line of lines) {
    const colon = line.indexOf(':');
    if (colon < 1) { return 'Each line must be Name: Value: "' + line + '"'; }
    const name = line.substring(0, colon).trim();
    if (!HEADER_NAME_RE.test(name)) { return 'Invalid header name: "' + name + '"'; }
  }
  return null;
}

function parseEnv(v) {
  return v.split(/\\r?\\n/).map(l => l.trim()).filter(l => l.length > 0);
}

function parseHeaders(v) {
  return v.split(/\\r?\\n/).map(l => l.trim()).filter(l => l.length > 0);
}

function updateDetected() {
  $('detected').textContent = detectTransport($('target').value);
}

function setError(field, msg) {
  $(field + '-err').textContent = msg || '';
}

function showBanner(msg) {
  const b = $('banner');
  b.textContent = msg;
  b.style.display = msg ? 'block' : 'none';
}

$('target').addEventListener('input', updateDetected);
$('cancel').addEventListener('click', () => vscode.postMessage({ type: 'cancel' }));

$('addForm').addEventListener('submit', (e) => {
  e.preventDefault();
  showBanner('');
  const name = $('name').value;
  const target = $('target').value;
  const envRaw = $('env').value;
  const headersRaw = $('headers').value;

  const nameErr = validateName(name);
  const targetErr = validateTarget(target);
  const envErr = validateEnv(envRaw);
  const headersErr = validateHeaders(headersRaw);
  setError('name', nameErr || '');
  setError('target', targetErr || '');
  setError('env', envErr || '');
  setError('headers', headersErr || '');
  if (nameErr || targetErr || envErr || headersErr) { return; }

  $('submit').disabled = true;
  vscode.postMessage({
    type: 'submit',
    payload: {
      name: name.trim(),
      target: target.trim(),
      transport: detectTransport(target),
      env: parseEnv(envRaw),
      headers: parseHeaders(headersRaw),
    },
  });
});

window.addEventListener('message', (event) => {
  const msg = event.data;
  if (!msg || typeof msg !== 'object') { return; }
  if (msg.type === 'nack') {
    showBanner(typeof msg.error === 'string' ? msg.error : 'Failed to add server.');
    $('submit').disabled = false;
  }
});

updateDetected();
</script>
</body>
</html>`;
}
