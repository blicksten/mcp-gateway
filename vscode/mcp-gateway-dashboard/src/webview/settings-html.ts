// Settings webview — HTML builder (Phase C T-C.1).
//
// Sticky header (sections) + scroll body + sticky footer (Save/Cancel) so
// the panel fits 800px viewport without horizontal scroll across the 25+
// mcpGateway.* settings (R-10). CSP nonce-locked: no `unsafe-inline`,
// `script-src` and `style-src` only allow our nonce + the webview's
// cspSource. Operator-supplied strings flow through textContent only —
// never innerHTML — so a malicious config value cannot execute.

import type { SettingsSchema, SettingsValues } from '../settings-schema';

/** Charcode-loop escapeHtml — no regex per CLAUDE.md "Regex Discipline". */
function escapeHtml(s: string): string {
	let out = '';
	for (let i = 0; i < s.length; i++) {
		const c = s.charAt(i);
		if (c === '&') { out += '&amp;'; }
		else if (c === '<') { out += '&lt;'; }
		else if (c === '>') { out += '&gt;'; }
		else if (c === '"') { out += '&quot;'; }
		else if (c === "'") { out += '&#39;'; }
		else { out += c; }
	}
	return out;
}

/** Safely embed a value as JSON inside a <script> tag.
 *  Escapes < and > to Unicode escapes so HTML parser cannot be tricked. */
function jsonForScript(value: unknown): string {
	let s = JSON.stringify(value);
	let out = '';
	for (let i = 0; i < s.length; i++) {
		const c = s.charAt(i);
		if (c === '&') { out += '\\u0026'; }
		else if (c === '<') { out += '\\u003c'; }
		else if (c === '>') { out += '\\u003e'; }
		else { out += c; }
	}
	return out;
}

interface BuildSettingsHtmlInput {
	nonce: string;
	cspSource: string;
	schema: SettingsSchema;
	currentValues: SettingsValues;
}

/** Build the settings webview HTML. The schema + values are injected as
 *  JSON into a <script> tag; rendering happens client-side via createElement
 *  so operator strings never reach an HTML parser path. */
export function buildSettingsHtml(input: BuildSettingsHtmlInput): string {
	const { nonce, cspSource, schema, currentValues } = input;
	const safeNonce = escapeHtml(nonce);
	const safeCsp = escapeHtml(cspSource);
	const schemaJson = jsonForScript(schema);
	const valuesJson = jsonForScript(currentValues);
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${safeCsp} 'nonce-${safeNonce}'; script-src 'nonce-${safeNonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>MCP Gateway — Settings</title>
<style nonce="${safeNonce}">
:root { --row-gap: 6px; --section-gap: 14px; }
* { box-sizing: border-box; }
html, body { height: 100%; margin: 0; }
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); background: var(--vscode-editor-background); display: flex; flex-direction: column; }
header { position: sticky; top: 0; z-index: 2; background: var(--vscode-editor-background); padding: 12px 16px 8px; border-bottom: 1px solid var(--vscode-panel-border); }
header h1 { font-size: 1.15em; margin: 0 0 6px 0; }
header .toolbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
header .toolbar input[type="text"] { flex: 1; min-width: 160px; padding: 4px 8px; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); border-radius: 2px; }
main { flex: 1; overflow-y: auto; padding: 12px 16px; min-height: 0; }
section { margin-bottom: var(--section-gap); border: 1px solid var(--vscode-panel-border); border-radius: 3px; padding: 8px 12px; background: var(--vscode-editorWidget-background); }
section h2 { font-size: 0.95em; margin: 0 0 6px 0; padding-bottom: 4px; border-bottom: 1px solid var(--vscode-panel-border); color: var(--vscode-descriptionForeground); }
.row { display: grid; grid-template-columns: minmax(180px, 1fr) 2fr; gap: 8px; align-items: start; padding: var(--row-gap) 0; border-bottom: 1px dotted var(--vscode-panel-border); }
.row:last-child { border-bottom: none; }
.row .label { font-size: 0.9em; padding-top: 4px; }
.row .label .key { font-family: var(--vscode-editor-font-family); font-size: 0.85em; color: var(--vscode-descriptionForeground); display: block; }
.row .label .restart-required { display: inline-block; margin-left: 4px; padding: 0 4px; font-size: 0.7em; border-radius: 2px; background: var(--vscode-editorWarning-foreground); color: var(--vscode-editor-background); vertical-align: text-top; }
.row .control { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.row .control .field-row { display: flex; gap: 4px; align-items: center; }
.row .control input[type="text"], .row .control input[type="number"], .row .control select { flex: 1; min-width: 0; padding: 4px 8px; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); border-radius: 2px; font-family: var(--vscode-editor-font-family); font-size: 0.9em; }
.row .control input[type="checkbox"] { margin: 0; }
.row .control button.browse { padding: 2px 10px; font-size: 0.85em; }
.row .control .description { font-size: 0.8em; color: var(--vscode-descriptionForeground); }
.row .control .validation { font-size: 0.8em; min-height: 1em; }
.row .control .validation.err { color: var(--vscode-errorForeground); }
.row .control .validation.ok { color: var(--vscode-testing-iconPassed); }
button { padding: 4px 12px; border: 1px solid var(--vscode-button-border, var(--vscode-panel-border)); background: var(--vscode-button-background); color: var(--vscode-button-foreground); cursor: pointer; border-radius: 2px; font-family: inherit; font-size: 0.9em; }
button:hover { background: var(--vscode-button-hoverBackground); }
button.secondary { background: var(--vscode-button-secondaryBackground); color: var(--vscode-button-secondaryForeground); }
button.secondary:hover { background: var(--vscode-button-secondaryHoverBackground); }
button:disabled { opacity: 0.5; cursor: default; }
footer { position: sticky; bottom: 0; z-index: 2; background: var(--vscode-editor-background); padding: 8px 16px; border-top: 1px solid var(--vscode-panel-border); display: flex; gap: 8px; justify-content: flex-end; align-items: center; }
footer .status { margin-right: auto; font-size: 0.85em; color: var(--vscode-descriptionForeground); }
.banner { padding: 6px 10px; border-radius: 2px; margin-bottom: 8px; font-size: 0.85em; display: none; }
.banner.err { background: var(--vscode-inputValidation-errorBackground); color: var(--vscode-inputValidation-errorForeground); border: 1px solid var(--vscode-inputValidation-errorBorder); }
.banner.warn { background: var(--vscode-inputValidation-warningBackground); color: var(--vscode-inputValidation-warningForeground); border: 1px solid var(--vscode-inputValidation-warningBorder); }
.banner.ok { background: var(--vscode-editorWidget-background); color: var(--vscode-foreground); border: 1px solid var(--vscode-testing-iconPassed); }
.row.hidden { display: none; }
section.hidden { display: none; }
</style>
</head>
<body>
<header>
  <h1>MCP Gateway — Settings</h1>
  <div class="toolbar">
    <input type="text" id="search" placeholder="Filter settings…" autocomplete="off" spellcheck="false">
    <button type="button" id="btnImport" class="secondary" title="Stage paths from mcpDashboard.* (review + Save manually)">Import paths from mcpDashboard</button>
    <button type="button" id="btnFillDefaults" title="One-click: write SAP Picker defaults (vibingPath, sapGuiPath, uvPath, mode=uv, keepassDbPath, sap-credentials.py) directly to user settings — no manual Save needed">Fill SAP defaults</button>
  </div>
  <div id="banner" class="banner" role="alert"></div>
</header>
<main id="form"></main>
<footer>
  <span class="status" id="status"></span>
  <button type="button" id="btnSave">Save</button>
  <button type="button" id="btnCancel" class="secondary">Close</button>
</footer>
<script nonce="${safeNonce}">
const vscode = acquireVsCodeApi();
const SCHEMA = ${schemaJson};
let values = ${valuesJson};
const edited = Object.create(null); // staged changes; flushed on Save
const validationErrors = Object.create(null);
let dirty = false;

const $ = (id) => document.getElementById(id);

function setBanner(kind, msg) {
  const b = $('banner');
  b.classList.remove('err', 'warn', 'ok');
  if (msg) {
    b.classList.add(kind);
    b.textContent = msg;
    b.style.display = 'block';
  } else {
    b.style.display = 'none';
    b.textContent = '';
  }
}

function setStatus(msg) {
  $('status').textContent = msg || '';
}

function getValue(key) {
  if (Object.prototype.hasOwnProperty.call(edited, key)) { return edited[key]; }
  return values[key];
}

function setEdited(key, value) {
  edited[key] = value;
  dirty = true;
  setStatus('Unsaved changes');
}

function buildField(field) {
  const row = document.createElement('div');
  row.className = 'row';
  row.dataset.key = field.key;
  row.dataset.label = (field.label + ' ' + field.key + ' ' + (field.description || '')).toLowerCase();

  const labelDiv = document.createElement('div');
  labelDiv.className = 'label';
  const labelStrong = document.createElement('strong');
  labelStrong.textContent = field.label;
  labelDiv.appendChild(labelStrong);
  if (field.restartRequired) {
    const tag = document.createElement('span');
    tag.className = 'restart-required';
    tag.textContent = 'restart';
    tag.title = 'Changing this setting requires a daemon restart';
    labelDiv.appendChild(tag);
  }
  const keySpan = document.createElement('span');
  keySpan.className = 'key';
  keySpan.textContent = field.key;
  labelDiv.appendChild(keySpan);

  const ctrlDiv = document.createElement('div');
  ctrlDiv.className = 'control';
  const fieldRow = document.createElement('div');
  fieldRow.className = 'field-row';

  const cur = getValue(field.key);
  if (field.type === 'boolean') {
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = Boolean(cur);
    cb.addEventListener('change', () => setEdited(field.key, cb.checked));
    fieldRow.appendChild(cb);
  } else if (field.type === 'number') {
    const inp = document.createElement('input');
    inp.type = 'number';
    inp.value = cur === undefined || cur === null ? '' : String(cur);
    if (typeof field.min === 'number') { inp.min = String(field.min); }
    inp.addEventListener('input', () => {
      const n = inp.value === '' ? undefined : Number(inp.value);
      setEdited(field.key, n);
    });
    fieldRow.appendChild(inp);
  } else if (field.type === 'enum') {
    const sel = document.createElement('select');
    for (const opt of (field.choices || [])) {
      const option = document.createElement('option');
      option.value = opt;
      option.textContent = opt;
      if (opt === cur) { option.selected = true; }
      sel.appendChild(option);
    }
    sel.addEventListener('change', () => setEdited(field.key, sel.value));
    fieldRow.appendChild(sel);
  } else {
    // string + path types
    const inp = document.createElement('input');
    inp.type = 'text';
    inp.spellcheck = false;
    inp.autocomplete = 'off';
    inp.value = typeof cur === 'string' ? cur : '';
    inp.addEventListener('input', () => {
      setEdited(field.key, inp.value);
      // Live validation only for path-typed fields
      if (field.kind === 'path') {
        vscode.postMessage({ type: 'validate', key: field.key, value: inp.value });
      }
    });
    fieldRow.appendChild(inp);
    if (field.kind === 'path') {
      const browseBtn = document.createElement('button');
      browseBtn.type = 'button';
      browseBtn.className = 'browse';
      browseBtn.textContent = 'Browse…';
      browseBtn.addEventListener('click', () => {
        vscode.postMessage({ type: 'browse', key: field.key, currentValue: inp.value });
      });
      fieldRow.appendChild(browseBtn);
    }
  }
  ctrlDiv.appendChild(fieldRow);

  if (field.description) {
    const desc = document.createElement('div');
    desc.className = 'description';
    desc.textContent = field.description;
    ctrlDiv.appendChild(desc);
  }
  const valDiv = document.createElement('div');
  valDiv.className = 'validation';
  valDiv.dataset.field = field.key;
  ctrlDiv.appendChild(valDiv);

  row.appendChild(labelDiv);
  row.appendChild(ctrlDiv);
  return row;
}

function render() {
  const form = $('form');
  while (form.firstChild) { form.removeChild(form.firstChild); }
  for (const section of SCHEMA.sections) {
    const sec = document.createElement('section');
    sec.dataset.sectionLabel = section.title.toLowerCase();
    const h2 = document.createElement('h2');
    h2.textContent = section.title;
    sec.appendChild(h2);
    for (const field of section.fields) {
      sec.appendChild(buildField(field));
    }
    form.appendChild(sec);
  }
}

function applyFilter() {
  const q = $('search').value.trim().toLowerCase();
  const sections = document.querySelectorAll('section[data-section-label]');
  for (const sec of sections) {
    let anyVisible = false;
    const rows = sec.querySelectorAll('.row');
    for (const row of rows) {
      const label = row.dataset.label || '';
      if (q === '' || label.includes(q)) {
        row.classList.remove('hidden');
        anyVisible = true;
      } else {
        row.classList.add('hidden');
      }
    }
    if (anyVisible) { sec.classList.remove('hidden'); } else { sec.classList.add('hidden'); }
  }
}

function setValidation(key, result) {
  validationErrors[key] = result && !result.ok;
  const el = document.querySelector('.validation[data-field="' + cssEscape(key) + '"]');
  if (!el) { return; }
  el.classList.remove('err', 'ok');
  el.textContent = '';
  if (!result) { return; }
  if (result.ok && result.message) {
    el.classList.add('ok');
    el.textContent = result.message;
  } else if (!result.ok) {
    el.classList.add('err');
    el.textContent = result.message || 'Invalid';
  }
}

// Minimal CSS-escape — only needed for attribute selector lookup; settings
// keys are dotted alphanum so a simple split/join on '.' suffices (C-03 fix:
// no regex per CLAUDE.md "Regex Discipline").
function cssEscape(s) {
  return s.split('.').join('\\\\.');
}

$('search').addEventListener('input', applyFilter);
$('btnImport').addEventListener('click', () => {
  vscode.postMessage({ type: 'importFromMcpDashboard' });
});
$('btnFillDefaults').addEventListener('click', () => {
  vscode.postMessage({ type: 'fillDefaults' });
});
$('btnSave').addEventListener('click', () => {
  if (!dirty) { setStatus('Nothing to save'); return; }
  // Send only changed fields
  const payload = {};
  for (const k of Object.keys(edited)) { payload[k] = edited[k]; }
  vscode.postMessage({ type: 'save', changes: payload });
});
$('btnCancel').addEventListener('click', () => {
  vscode.postMessage({ type: 'cancel' });
});

window.addEventListener('message', (event) => {
  const msg = event.data;
  if (!msg || typeof msg !== 'object') { return; }
  if (msg.type === 'init') {
    if (msg.values && typeof msg.values === 'object') { values = msg.values; }
    for (const k of Object.keys(edited)) { delete edited[k]; }
    dirty = false;
    render();
    setStatus('');
  } else if (msg.type === 'validation') {
    if (typeof msg.key === 'string') { setValidation(msg.key, msg.result); }
  } else if (msg.type === 'browseResult') {
    if (typeof msg.key === 'string' && typeof msg.path === 'string') {
      const inp = document.querySelector('.row[data-key="' + cssEscape(msg.key) + '"] input[type="text"]');
      if (inp) {
        inp.value = msg.path;
        setEdited(msg.key, msg.path);
      }
    }
  } else if (msg.type === 'saved') {
    // Apply committed values into the canonical map; drop edits
    if (msg.values && typeof msg.values === 'object') { values = msg.values; }
    for (const k of Object.keys(edited)) { delete edited[k]; }
    dirty = false;
    setStatus(typeof msg.status === 'string' ? msg.status : 'Saved');
    setBanner(msg.banner === 'warn' ? 'warn' : 'ok', typeof msg.summary === 'string' ? msg.summary : 'Settings saved');
  } else if (msg.type === 'imported') {
    // The host applied non-overwriting fills into our staging map. Re-render
    // so the inputs reflect the new staged values; user must Save to commit.
    if (msg.staged && typeof msg.staged === 'object') {
      for (const [k, v] of Object.entries(msg.staged)) { edited[k] = v; }
      dirty = true;
    }
    render();
    const n = typeof msg.count === 'number' ? msg.count : 0;
    setBanner('ok', 'Imported ' + n + ' path(s) from mcpDashboard. Review and Save.');
    setStatus('Unsaved changes (imported)');
  } else if (msg.type === 'error') {
    setBanner('err', typeof msg.message === 'string' ? msg.message : 'Error');
  }
});

render();
</script>
</body>
</html>`;
}
