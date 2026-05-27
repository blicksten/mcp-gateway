// SAP Picker — webview HTML builder (Phase B T-B.1..T-B.4).
//
// Kept in its own module rather than appended to html-builder.ts to bound
// review surface — this template is ~280 lines of CSS + JS that only the
// SapPickerPanel needs to load. CSP is nonce-locked (no inline-unsafe);
// every operator-supplied string flows through textContent or escaped
// JSON-for-script — never innerHTML.

/** Escape HTML special characters — string methods only per
 *  CLAUDE.md "Regex Discipline (MANDATORY)". */
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

export function buildSapPickerHtml(nonce: string, cspSource: string): string {
	const safeNonce = escapeHtml(nonce);
	const safeCsp = escapeHtml(cspSource);
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${safeCsp} 'nonce-${safeNonce}'; script-src 'nonce-${safeNonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SAP Picker</title>
<style nonce="${safeNonce}">
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); background: var(--vscode-editor-background); padding: 12px; margin: 0; }
h1 { font-size: 1.2em; margin: 0 0 8px 0; }
.toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; margin-bottom: 12px; }
.toolbar .filter-group { display: flex; gap: 12px; }
.toolbar .filter-group label { display: flex; align-items: center; gap: 4px; font-size: 0.9em; cursor: pointer; }
.toolbar .search { flex: 1; min-width: 160px; }
.toolbar input[type="text"] { width: 100%; padding: 4px 8px; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); border-radius: 2px; font-family: var(--vscode-editor-font-family); }
.toolbar .actions { display: flex; gap: 6px; }
button { padding: 4px 12px; border: 1px solid var(--vscode-button-border, var(--vscode-panel-border)); background: var(--vscode-button-background); color: var(--vscode-button-foreground); cursor: pointer; border-radius: 2px; font-family: inherit; font-size: 0.9em; }
button:hover { background: var(--vscode-button-hoverBackground); }
button.secondary { background: var(--vscode-button-secondaryBackground); color: var(--vscode-button-secondaryForeground); }
button.secondary:hover { background: var(--vscode-button-secondaryHoverBackground); }
button:disabled { opacity: 0.5; cursor: default; }
.banner { padding: 6px 10px; border-radius: 2px; margin-bottom: 8px; font-size: 0.85em; display: none; }
.banner.err { background: var(--vscode-inputValidation-errorBackground); color: var(--vscode-inputValidation-errorForeground); border: 1px solid var(--vscode-inputValidation-errorBorder); }
.banner.warn { background: var(--vscode-inputValidation-warningBackground); color: var(--vscode-inputValidation-warningForeground); border: 1px solid var(--vscode-inputValidation-warningBorder); }
.banner.info { background: var(--vscode-editorWidget-background); color: var(--vscode-foreground); border: 1px solid var(--vscode-panel-border); }
.banner.ok { background: var(--vscode-editorWidget-background); color: var(--vscode-foreground); border: 1px solid var(--vscode-testing-iconPassed); }
table { border-collapse: collapse; width: 100%; }
thead th { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--vscode-panel-border); position: sticky; top: 0; background: var(--vscode-editor-background); z-index: 1; font-weight: 600; font-size: 0.85em; }
tbody tr { content-visibility: auto; contain-intrinsic-size: auto 32px; }
tbody td { padding: 4px 8px; border-bottom: 1px solid var(--vscode-panel-border); font-size: 0.9em; vertical-align: middle; }
tbody tr.kpmissing { color: var(--vscode-descriptionForeground); }
tbody tr.kpmissing td.cred { color: var(--vscode-editorWarning-foreground); }
.checkbox-cell { white-space: nowrap; }
.checkbox-cell input[type="checkbox"]:disabled + .disabled-mark { display: inline-block; }
.disabled-mark { display: none; color: var(--vscode-descriptionForeground); margin-left: 2px; }
.row-status { display: inline-block; padding: 1px 6px; border-radius: 2px; font-size: 0.75em; margin-left: 4px; }
.row-status.idle { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); }
.row-status.in_progress, .row-status.pending { background: var(--vscode-progressBar-background, var(--vscode-badge-background)); color: var(--vscode-button-foreground); }
.row-status.config_added, .row-status.config_added_running, .row-status.removed { background: var(--vscode-testing-iconPassed); color: var(--vscode-editor-background); }
.row-status.config_added_start_failed, .row-status.removed_with_orphan, .row-status.removal_failed { background: var(--vscode-testing-iconFailed); color: var(--vscode-editor-background); }
.expand-btn { cursor: pointer; background: none; border: 1px solid transparent; padding: 0 6px; color: var(--vscode-foreground); }
.expand-btn:hover { background: var(--vscode-toolbar-hoverBackground); border: 1px solid var(--vscode-panel-border); }
.expand-row td { background: var(--vscode-editorWidget-background); padding: 8px 16px; }
.expand-pane { display: flex; flex-direction: column; gap: 6px; }
.expand-pane label { display: flex; flex-direction: column; gap: 2px; font-size: 0.85em; }
.expand-pane input[type="text"] { padding: 3px 6px; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); border-radius: 2px; font-family: var(--vscode-editor-font-family); }
.error-text { color: var(--vscode-errorForeground); font-size: 0.8em; }
.force-kill { font-size: 0.75em; color: var(--vscode-errorForeground); margin-left: 6px; cursor: pointer; text-decoration: underline; background: none; border: none; padding: 0; }
.empty { padding: 16px; color: var(--vscode-descriptionForeground); font-style: italic; text-align: center; }
.degenerate-hint { font-size: 0.85em; color: var(--vscode-descriptionForeground); padding: 6px 0; }
</style>
</head>
<body>
<h1>SAP Picker</h1>
<div id="banner" class="banner" role="alert"></div>
<div id="warnings" class="banner warn" role="status"></div>
<div id="degenerate" class="degenerate-hint" hidden>Filter restored to "Available + No credentials" — at least one filter must remain on.</div>

<div class="toolbar">
  <div class="filter-group" role="group" aria-label="Filter">
    <label><input type="checkbox" id="f-registered" checked> Registered</label>
    <label><input type="checkbox" id="f-available" checked> Available</label>
    <label><input type="checkbox" id="f-nocred" checked> No credentials</label>
  </div>
  <div class="search">
    <input type="text" id="search" placeholder="Search SID, Client, User…" autocomplete="off" spellcheck="false">
  </div>
  <div class="actions">
    <button id="btnApply">Apply changes</button>
    <button id="btnRetry" class="secondary">Retry failed rows</button>
    <button id="btnRefresh" class="secondary">Refresh</button>
    <button id="btnCancel" class="secondary">Close</button>
  </div>
</div>

<table id="picker">
  <thead>
    <tr>
      <th>SID</th>
      <th>Client</th>
      <th>User</th>
      <th>VSP</th>
      <th>GUI</th>
      <th></th>
    </tr>
  </thead>
  <tbody id="rows"></tbody>
</table>
<div id="empty" class="empty" hidden>No SAP systems match the current filter.</div>

<script nonce="${safeNonce}">
const vscode = acquireVsCodeApi();
const $ = (id) => document.getElementById(id);

// Webview-owned UI state. Snapshot fields (sid/client/registered/status/
// kpMissing) are read-only and never written here. Desired and override are
// the user-editable fields the host re-validates on Apply.
let rowMap = new Map(); // rowKey -> row (from host serialization)
let editedDesired = new Map(); // rowKey -> {vsp,gui}
let editedOverride = new Map(); // rowKey -> {vspCommand,guiCommand,guiUvProject}
let expandSet = new Set(); // expandKey: \`\${sid}-\${client}-\${component}\`
let filter = { registered: true, available: true, noCredentials: true };
let search = '';
let applying = false;

// --- Pure helpers (mirrors of sap-picker-state.ts subset) ---

function rowKey(row) {
  var user = row.user || '';
  if (!row.client && !user) return row.sid;
  if (!user) return row.sid + '-' + row.client;
  return row.sid + '-' + row.client + '-' + user;
}
function expandKey(sid, client, component) { return sid + '-' + client + '-' + component; }
function categorize(row) {
  if (row.kpMissing) { return 'no-credentials'; }
  if (row.registered.vsp || row.registered.gui) { return 'registered'; }
  return 'available';
}
function applyFilter(rows, f, q) {
  const trimmed = q.trim().toLowerCase();
  return rows.filter((r) => {
    const cat = categorize(r);
    if (cat === 'registered' && !f.registered) { return false; }
    if (cat === 'available' && !f.available) { return false; }
    if (cat === 'no-credentials' && !f.noCredentials) { return false; }
    if (trimmed.length === 0) { return true; }
    return r.sid.toLowerCase().includes(trimmed)
      || r.client.toLowerCase().includes(trimmed)
      || (r.user || '').toLowerCase().includes(trimmed);
  });
}
function degenerateGuard(f) {
  if (!f.registered && !f.available && !f.noCredentials) {
    return { filter: { registered: false, available: true, noCredentials: true }, restored: true };
  }
  return { filter: f, restored: false };
}

// --- Render ---

function statusLabel(s) {
  if (s === 'idle') { return ''; } // suppress noise
  if (s === 'pending') { return 'queued'; }
  if (s === 'in_progress') { return 'working…'; }
  if (s === 'config_added') { return 'added'; }
  if (s === 'config_added_running') { return 'running'; }
  if (s === 'config_added_start_failed') { return 'start failed'; }
  if (s === 'removed') { return 'removed'; }
  if (s === 'removed_with_orphan') { return 'orphan!'; }
  if (s === 'removal_failed') { return 'remove failed'; }
  return s;
}

function buildStatusBadge(status) {
  const label = statusLabel(status);
  if (!label) { return null; }
  const span = document.createElement('span');
  span.className = 'row-status ' + status;
  span.textContent = label;
  return span;
}

function buildRow(row) {
  const tr = document.createElement('tr');
  if (row.kpMissing) { tr.className = 'kpmissing'; }
  tr.dataset.rowKey = row.key;

  // SID
  const tdSid = document.createElement('td');
  tdSid.textContent = row.sid;
  tr.appendChild(tdSid);

  // Client
  const tdClient = document.createElement('td');
  tdClient.textContent = row.client || '(none)';
  tr.appendChild(tdClient);

  // User
  const tdUser = document.createElement('td');
  tdUser.className = 'cred';
  if (row.kpMissing) { tdUser.textContent = '⊘ no credentials'; }
  else { tdUser.textContent = row.user || ''; }
  tr.appendChild(tdUser);

  // VSP checkbox + status
  tr.appendChild(buildComponentCell(row, 'vsp'));

  // GUI checkbox + status
  tr.appendChild(buildComponentCell(row, 'gui'));

  // Expand control
  const tdExpand = document.createElement('td');
  const expandBtn = document.createElement('button');
  expandBtn.className = 'expand-btn';
  expandBtn.textContent = '⋮';
  expandBtn.setAttribute('aria-label', 'Toggle row details');
  expandBtn.addEventListener('click', () => toggleExpand(row));
  tdExpand.appendChild(expandBtn);
  tr.appendChild(tdExpand);

  return tr;
}

function buildComponentCell(row, component) {
  const td = document.createElement('td');
  td.className = 'checkbox-cell';

  const cb = document.createElement('input');
  cb.type = 'checkbox';
  const desired = editedDesired.get(row.key) || { vsp: row.desired.vsp, gui: row.desired.gui };
  cb.checked = component === 'vsp' ? Boolean(desired.vsp) : Boolean(desired.gui);
  cb.disabled = row.kpMissing || applying;
  cb.setAttribute('aria-label', component.toUpperCase() + ' for ' + row.sid + '-' + row.client);
  cb.addEventListener('change', () => onCheckboxChange(row, component, cb.checked));
  td.appendChild(cb);

  if (row.kpMissing) {
    const mark = document.createElement('span');
    mark.className = 'disabled-mark';
    mark.textContent = '⊘';
    mark.title = 'Add SID to KeePass first — \`mcp-ctl credential import\` or KeePassXC.';
    td.appendChild(mark);
  }

  const status = component === 'vsp' ? row.vspStatus : row.guiStatus;
  const error = component === 'vsp' ? row.vspError : row.guiError;
  const badge = buildStatusBadge(status);
  if (badge) { td.appendChild(badge); }
  if (error) {
    const err = document.createElement('div');
    err.className = 'error-text';
    err.textContent = error;
    td.appendChild(err);
  }
  if (status === 'removed_with_orphan') {
    const fk = document.createElement('button');
    fk.type = 'button';
    fk.className = 'force-kill';
    fk.textContent = 'Force kill';
    fk.addEventListener('click', () => forceKill(row.key, component));
    td.appendChild(fk);
  }
  return td;
}

function buildExpandRow(row) {
  // Build a single expand row containing override panes for whichever
  // components currently have their expandKey set. Survives filter changes
  // because expandSet is keyed by sid+client+component, not row index.
  const tr = document.createElement('tr');
  tr.className = 'expand-row';
  const td = document.createElement('td');
  td.colSpan = 6;
  const pane = document.createElement('div');
  pane.className = 'expand-pane';

  const ov = editedOverride.get(row.key) || row.override || {};
  // VSP command
  if (expandSet.has(expandKey(row.sid, row.client, 'vsp'))) {
    pane.appendChild(buildOverrideInput(row, 'VSP command', 'vspCommand', ov.vspCommand || ''));
  }
  // GUI command
  if (expandSet.has(expandKey(row.sid, row.client, 'gui'))) {
    pane.appendChild(buildOverrideInput(row, 'GUI command', 'guiCommand', ov.guiCommand || ''));
    pane.appendChild(buildOverrideInput(row, 'GUI uv project (optional)', 'guiUvProject', ov.guiUvProject || ''));
  }

  td.appendChild(pane);
  tr.appendChild(td);
  return tr;
}

function buildOverrideInput(row, labelText, field, value) {
  const label = document.createElement('label');
  const span = document.createElement('span');
  span.textContent = labelText;
  label.appendChild(span);
  const input = document.createElement('input');
  input.type = 'text';
  input.value = value;
  input.spellcheck = false;
  input.autocomplete = 'off';
  input.disabled = applying || row.kpMissing;
  input.addEventListener('input', () => {
    const cur = editedOverride.get(row.key) || {};
    const next = Object.assign({}, cur);
    next[field] = input.value;
    editedOverride.set(row.key, next);
  });
  label.appendChild(input);
  return label;
}

function rowExpanded(row) {
  // Row is expanded if either component's expandKey is in the set.
  return expandSet.has(expandKey(row.sid, row.client, 'vsp'))
    || expandSet.has(expandKey(row.sid, row.client, 'gui'));
}

function render() {
  const tbody = $('rows');
  // Replace children (rather than innerHTML) so we never reflow operator
  // strings through HTML parsing.
  while (tbody.firstChild) { tbody.removeChild(tbody.firstChild); }

  const allRows = Array.from(rowMap.values());
  const visible = applyFilter(allRows, filter, search);

  if (visible.length === 0) {
    $('empty').hidden = false;
  } else {
    $('empty').hidden = true;
  }

  for (const row of visible) {
    tbody.appendChild(buildRow(row));
    if (rowExpanded(row)) { tbody.appendChild(buildExpandRow(row)); }
  }

  // Update filter checkboxes to match (in case degenerate guard changed them)
  $('f-registered').checked = filter.registered;
  $('f-available').checked = filter.available;
  $('f-nocred').checked = filter.noCredentials;
  // Buttons enable/disable
  const hasFailedRows = allRows.some((r) =>
    r.vspStatus === 'config_added_start_failed' ||
    r.vspStatus === 'removed_with_orphan' ||
    r.vspStatus === 'removal_failed' ||
    r.guiStatus === 'config_added_start_failed' ||
    r.guiStatus === 'removed_with_orphan' ||
    r.guiStatus === 'removal_failed');
  $('btnApply').disabled = applying || allRows.length === 0;
  $('btnRetry').disabled = applying || !hasFailedRows;
  $('btnRefresh').disabled = applying;
}

function onCheckboxChange(row, component, checked) {
  if (row.kpMissing) { return; } // R-30 defence — UI guard
  const cur = editedDesired.get(row.key) || { vsp: row.desired.vsp, gui: row.desired.gui };
  const next = { vsp: cur.vsp, gui: cur.gui };
  if (component === 'vsp') { next.vsp = checked; } else { next.gui = checked; }
  editedDesired.set(row.key, next);
}

function toggleExpand(row) {
  // Toggle BOTH component keys in lockstep — the panel renders a single
  // expand row holding both components, but the keys are stored separately
  // so a future per-component expand UI can light them up independently.
  const vspKey = expandKey(row.sid, row.client, 'vsp');
  const guiKey = expandKey(row.sid, row.client, 'gui');
  const expanded = expandSet.has(vspKey) || expandSet.has(guiKey);
  if (expanded) {
    expandSet.delete(vspKey);
    expandSet.delete(guiKey);
  } else {
    expandSet.add(vspKey);
    expandSet.add(guiKey);
  }
  render();
}

function showBanner(id, kind, msg) {
  const b = $(id);
  b.classList.remove('err', 'warn', 'info', 'ok');
  b.classList.add(kind);
  b.textContent = msg;
  b.style.display = msg ? 'block' : 'none';
}

function clearBanner(id) {
  const b = $(id);
  b.style.display = 'none';
  b.textContent = '';
}

function buildDiffsForApply(allRows) {
  const diffs = [];
  for (const r of allRows) {
    const desired = editedDesired.get(r.key) || { vsp: r.desired.vsp, gui: r.desired.gui };
    const override = editedOverride.get(r.key) || r.override || {};
    diffs.push({
      rowKey: r.key,
      desired: { vsp: Boolean(desired.vsp), gui: Boolean(desired.gui) },
      override: {
        vspCommand: typeof override.vspCommand === 'string' ? override.vspCommand : '',
        guiCommand: typeof override.guiCommand === 'string' ? override.guiCommand : '',
        guiUvProject: typeof override.guiUvProject === 'string' ? override.guiUvProject : '',
      },
    });
  }
  return diffs;
}

function forceKill(rowKey, component) {
  vscode.postMessage({ type: 'forceKill', rowKey: rowKey, component: component });
}

// --- Wire toolbar ---

$('f-registered').addEventListener('change', () => {
  filter.registered = $('f-registered').checked;
  const g = degenerateGuard(filter);
  filter = g.filter;
  $('degenerate').hidden = !g.restored;
  render();
});
$('f-available').addEventListener('change', () => {
  filter.available = $('f-available').checked;
  const g = degenerateGuard(filter);
  filter = g.filter;
  $('degenerate').hidden = !g.restored;
  render();
});
$('f-nocred').addEventListener('change', () => {
  filter.noCredentials = $('f-nocred').checked;
  const g = degenerateGuard(filter);
  filter = g.filter;
  $('degenerate').hidden = !g.restored;
  render();
});

// 16ms keystroke budget at 100 rows — render() runs in a single sync pass;
// no debouncing needed at this row count.
$('search').addEventListener('input', () => { search = $('search').value; render(); });

$('btnApply').addEventListener('click', () => {
  if (applying) { return; }
  clearBanner('banner');
  const allRows = Array.from(rowMap.values());
  vscode.postMessage({ type: 'apply', diffs: buildDiffsForApply(allRows) });
});
$('btnRetry').addEventListener('click', () => {
  if (applying) { return; }
  clearBanner('banner');
  const allRows = Array.from(rowMap.values());
  vscode.postMessage({ type: 'retryFailed', diffs: buildDiffsForApply(allRows) });
});
$('btnRefresh').addEventListener('click', () => {
  if (applying) { return; }
  vscode.postMessage({ type: 'refresh' });
});
$('btnCancel').addEventListener('click', () => {
  vscode.postMessage({ type: 'cancel' });
});

// --- Inbound messages ---

window.addEventListener('message', (event) => {
  const msg = event.data;
  if (!msg || typeof msg !== 'object') { return; }
  if (msg.type === 'init') {
    rowMap = new Map();
    if (Array.isArray(msg.rows)) {
      for (const r of msg.rows) {
        if (r && typeof r === 'object' && typeof r.key === 'string') {
          rowMap.set(r.key, r);
        }
      }
    }
    // Reset edits on full re-init (refresh / refresh-after-apply); the
    // freshly-fetched snapshot becomes the new baseline.
    editedDesired = new Map();
    editedOverride = new Map();
    // Surface snapshot warnings at the top.
    const warnings = Array.isArray(msg.warnings) ? msg.warnings.filter((w) => typeof w === 'string') : [];
    if (warnings.length > 0) {
      showBanner('warnings', 'warn', warnings.join(' · '));
    } else {
      clearBanner('warnings');
    }
    render();
  } else if (msg.type === 'rows') {
    if (Array.isArray(msg.rows)) {
      for (const r of msg.rows) {
        if (r && typeof r === 'object' && typeof r.key === 'string') {
          rowMap.set(r.key, r);
        }
      }
    }
    render();
  } else if (msg.type === 'applying') {
    applying = Boolean(msg.active);
    render();
  } else if (msg.type === 'applied') {
    const ok = typeof msg.ok === 'number' ? msg.ok : 0;
    const failed = typeof msg.failed === 'number' ? msg.failed : 0;
    const summary = typeof msg.summary === 'string' ? msg.summary : '';
    showBanner('banner', failed === 0 ? 'ok' : 'warn', summary || (failed === 0 ? ok + ' applied.' : ok + ' applied, ' + failed + ' failed.'));
  } else if (msg.type === 'error') {
    showBanner('banner', 'err', typeof msg.message === 'string' ? msg.message : 'An error occurred.');
  }
});

// Initial empty render so the table chrome is visible while the host fetches
// the snapshot.
render();
</script>
</body>
</html>`;
}
