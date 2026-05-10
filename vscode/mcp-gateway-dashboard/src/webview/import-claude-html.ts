// Import-from-Claude — webview HTML builder (Phase E T-E.1..T-E.3).
//
// Mirrors webview/sap-picker-html.ts: CSP nonce-locked, all operator strings
// flow through textContent or escaped JSON-for-script (never innerHTML).
// Single-pass synchronous render for the 16ms keystroke budget.

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

export function buildImportClaudeHtml(nonce: string, cspSource: string): string {
	const safeNonce = escapeHtml(nonce);
	const safeCsp = escapeHtml(cspSource);
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy"
      content="default-src 'none'; style-src ${safeCsp} 'nonce-${safeNonce}'; script-src 'nonce-${safeNonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Import from Claude</title>
<style nonce="${safeNonce}">
body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); background: var(--vscode-editor-background); padding: 12px; margin: 0; }
h1 { font-size: 1.2em; margin: 0 0 8px 0; }
.toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; margin-bottom: 12px; }
.toolbar .source-group, .toolbar .filter-group { display: flex; gap: 12px; align-items: center; }
.toolbar .source-group label, .toolbar .filter-group label { display: flex; align-items: center; gap: 4px; font-size: 0.9em; cursor: pointer; }
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
.path-line { font-family: var(--vscode-editor-font-family); font-size: 0.8em; color: var(--vscode-descriptionForeground); padding: 4px 0; }
table { border-collapse: collapse; width: 100%; }
thead th { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--vscode-panel-border); position: sticky; top: 0; background: var(--vscode-editor-background); z-index: 1; font-weight: 600; font-size: 0.85em; }
tbody tr { content-visibility: auto; contain-intrinsic-size: auto 32px; }
tbody td { padding: 4px 8px; border-bottom: 1px solid var(--vscode-panel-border); font-size: 0.9em; vertical-align: middle; }
tbody tr.gateway-collision { background: var(--vscode-editorWarning-background, transparent); }
.row-status { display: inline-block; padding: 1px 6px; border-radius: 2px; font-size: 0.75em; margin-left: 4px; }
.row-status.idle { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); }
.row-status.in_progress, .row-status.pending { background: var(--vscode-progressBar-background, var(--vscode-badge-background)); color: var(--vscode-button-foreground); }
.row-status.applied { background: var(--vscode-testing-iconPassed); color: var(--vscode-editor-background); }
.row-status.skipped { background: var(--vscode-descriptionForeground); color: var(--vscode-editor-background); }
.row-status.conflict, .row-status.error { background: var(--vscode-testing-iconFailed); color: var(--vscode-editor-background); }
.provenance-badge { display: inline-block; font-size: 0.75em; color: var(--vscode-descriptionForeground); margin-left: 6px; }
.drift-badge { display: inline-block; font-size: 0.75em; color: var(--vscode-editorWarning-foreground); margin-left: 6px; cursor: help; border-bottom: 1px dotted var(--vscode-editorWarning-foreground); }
.action-cell { white-space: nowrap; }
.action-cell select { padding: 2px 4px; background: var(--vscode-dropdown-background); color: var(--vscode-dropdown-foreground); border: 1px solid var(--vscode-dropdown-border, var(--vscode-panel-border)); border-radius: 2px; font-size: 0.85em; }
.dest-cell input { width: 140px; padding: 2px 4px; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); border-radius: 2px; font-size: 0.85em; }
.error-text { color: var(--vscode-errorForeground); font-size: 0.8em; }
.empty { padding: 16px; color: var(--vscode-descriptionForeground); font-style: italic; text-align: center; }
.degenerate-hint { font-size: 0.85em; color: var(--vscode-descriptionForeground); padding: 6px 0; }
.preview-modal { position: fixed; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; z-index: 100; }
.preview-modal.hidden { display: none; }
.preview-modal-inner { background: var(--vscode-editor-background); color: var(--vscode-foreground); border: 1px solid var(--vscode-panel-border); padding: 16px; max-width: 720px; max-height: 80vh; overflow: auto; border-radius: 4px; }
.preview-modal-inner h2 { font-size: 1.1em; margin: 0 0 8px 0; }
.preview-modal-inner .destructive-warning { background: var(--vscode-inputValidation-errorBackground); color: var(--vscode-inputValidation-errorForeground); border: 1px solid var(--vscode-inputValidation-errorBorder); padding: 6px 10px; margin-bottom: 12px; font-size: 0.9em; border-radius: 2px; }
.preview-modal-inner table { margin-bottom: 12px; }
.preview-modal-inner .modal-actions { display: flex; gap: 8px; justify-content: flex-end; }
</style>
</head>
<body>
<h1>Import from Claude</h1>
<div id="banner" class="banner" role="alert"></div>
<div id="warnings" class="banner warn" role="status"></div>
<div id="moveOverwriteRisk" class="banner err" role="alert" hidden></div>
<div id="degenerate" class="degenerate-hint" hidden>Filter restored to "Available + Drift" — at least one filter must remain on.</div>

<div class="toolbar">
  <div class="source-group" role="group" aria-label="Source">
    <strong>Source:</strong>
    <label><input type="radio" name="source" value="cc_global" checked> Claude global</label>
    <label><input type="radio" name="source" value="cc_project"> Project (.mcp.json)</label>
    <label><input type="radio" name="source" value="desktop"> Claude Desktop</label>
  </div>
</div>
<div id="path-line" class="path-line"></div>

<div class="toolbar">
  <div class="filter-group" role="group" aria-label="Filter">
    <label><input type="checkbox" id="f-gateway-only" checked> Gateway has name</label>
    <label><input type="checkbox" id="f-available" checked> Available</label>
    <label><input type="checkbox" id="f-drift" checked> Drift</label>
  </div>
  <div class="search">
    <input type="text" id="search" placeholder="Search name, command, url…" autocomplete="off" spellcheck="false">
  </div>
  <div class="actions">
    <button id="btnPreview">Preview</button>
    <button id="btnApply">Apply</button>
    <button id="btnRetry" class="secondary">Retry failed</button>
    <button id="btnRefresh" class="secondary">Refresh</button>
    <button id="btnCancel" class="secondary">Close</button>
  </div>
</div>

<table id="picker">
  <thead>
    <tr>
      <th></th>
      <th>Name</th>
      <th>Type</th>
      <th>Command / URL</th>
      <th>Action</th>
      <th>Conflict</th>
      <th>Dest name (optional)</th>
      <th>Status</th>
    </tr>
  </thead>
  <tbody id="rows"></tbody>
</table>
<div id="empty" class="empty" hidden>No entries match the current filter.</div>

<div id="previewModal" class="preview-modal hidden" aria-modal="true" role="dialog">
  <div class="preview-modal-inner">
    <h2 id="previewTitle">Preview</h2>
    <div id="previewDestructiveWarning" class="destructive-warning" hidden></div>
    <div id="previewBody"></div>
    <div class="modal-actions">
      <button id="btnConfirmApply">Confirm + Apply</button>
      <button id="btnCancelPreview" class="secondary">Cancel</button>
    </div>
  </div>
</div>

<script nonce="${safeNonce}">
const vscode = acquireVsCodeApi();
const $ = (id) => document.getElementById(id);

let rowMap = new Map(); // rowKey -> row (from host serialization)
let edits = new Map();  // rowKey -> {checked,action,conflict,destName}
let filter = { gatewayOnly: true, available: true, drift: true };
let search = '';
let applying = false;
let pendingPreviewOps = null; // ops list awaiting confirm in modal
let currentSource = 'cc_global';

// --- Pure helpers (mirrors of import-claude-state.ts subset) ---

function importRowKey(source, name) { return source + '::' + name; }
function categorize(row) {
  if (row.gateway_has_name && Array.isArray(row.drift_fields) && row.drift_fields.length > 0) {
    return 'drift';
  }
  if (row.gateway_has_name) { return 'gateway-only'; }
  return 'available';
}
function applyFilter(rows, f, q) {
  const trimmed = q.trim().toLowerCase();
  return rows.filter((r) => {
    const cat = categorize(r);
    if (cat === 'gateway-only' && !f.gatewayOnly) { return false; }
    if (cat === 'available' && !f.available) { return false; }
    if (cat === 'drift' && !f.drift) { return false; }
    if (trimmed.length === 0) { return true; }
    return r.name.toLowerCase().includes(trimmed)
      || (r.command || '').toLowerCase().includes(trimmed)
      || (r.url || '').toLowerCase().includes(trimmed);
  });
}
function degenerateGuard(f) {
  if (!f.gatewayOnly && !f.available && !f.drift) {
    return { filter: { gatewayOnly: false, available: true, drift: true }, restored: true };
  }
  return { filter: f, restored: false };
}
function getEdit(rowKey, snapshotDefaults) {
  const e = edits.get(rowKey);
  if (e) { return e; }
  return {
    checked: false,
    action: 'copy',
    conflict: 'skip',
    destName: '',
  };
}

// --- Render ---

function statusLabel(s) {
  if (s === 'idle') { return ''; }
  if (s === 'pending') { return 'queued'; }
  if (s === 'in_progress') { return 'working…'; }
  if (s === 'applied') { return 'applied'; }
  if (s === 'skipped') { return 'skipped'; }
  if (s === 'conflict') { return 'conflict'; }
  if (s === 'error') { return 'error'; }
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
  tr.dataset.rowKey = row.key;
  if (row.gateway_has_name) { tr.classList.add('gateway-collision'); }
  const ed = getEdit(row.key);

  // Checkbox
  const tdCheck = document.createElement('td');
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = Boolean(ed.checked);
  cb.disabled = applying;
  cb.setAttribute('aria-label', 'Include ' + row.name);
  cb.addEventListener('change', () => {
    const cur = getEdit(row.key);
    edits.set(row.key, Object.assign({}, cur, { checked: cb.checked }));
    updateMoveOverwriteRiskBanner();
    updateButtons();
  });
  tdCheck.appendChild(cb);
  tr.appendChild(tdCheck);

  // Name + provenance + drift badges
  const tdName = document.createElement('td');
  const nameSpan = document.createElement('span');
  nameSpan.textContent = row.name;
  tdName.appendChild(nameSpan);
  if (row.previously_imported) {
    const prov = document.createElement('span');
    prov.className = 'provenance-badge';
    prov.textContent = '◊ previously imported';
    if (typeof row.previously_imported_at === 'string' && row.previously_imported_at.length > 0) {
      prov.title = 'Imported at: ' + row.previously_imported_at;
    }
    tdName.appendChild(prov);
  }
  if (row.gateway_has_name && Array.isArray(row.drift_fields) && row.drift_fields.length > 0) {
    const drift = document.createElement('span');
    drift.className = 'drift-badge';
    drift.textContent = '⚠ drift: ' + row.drift_fields.join(', ');
    drift.title = 'Gateway entry differs in: ' + row.drift_fields.join(', ');
    tdName.appendChild(drift);
  } else if (row.gateway_has_name) {
    const collide = document.createElement('span');
    collide.className = 'provenance-badge';
    collide.textContent = '◇ name in use';
    tdName.appendChild(collide);
  }
  tr.appendChild(tdName);

  // Type
  const tdType = document.createElement('td');
  tdType.textContent = row.type || '';
  tr.appendChild(tdType);

  // Command / URL preview
  const tdCmd = document.createElement('td');
  const cmdText = row.command
    ? (row.command + (Array.isArray(row.args) && row.args.length > 0 ? ' ' + row.args.join(' ') : ''))
    : (row.url || '');
  tdCmd.textContent = cmdText;
  tdCmd.title = cmdText;
  tdCmd.style.maxWidth = '300px';
  tdCmd.style.overflow = 'hidden';
  tdCmd.style.textOverflow = 'ellipsis';
  tdCmd.style.whiteSpace = 'nowrap';
  tr.appendChild(tdCmd);

  // Action select
  const tdAction = document.createElement('td');
  tdAction.className = 'action-cell';
  const actSel = document.createElement('select');
  actSel.disabled = applying;
  for (const v of ['copy', 'move']) {
    const opt = document.createElement('option');
    opt.value = v;
    opt.textContent = v;
    if (v === ed.action) { opt.selected = true; }
    actSel.appendChild(opt);
  }
  actSel.addEventListener('change', () => {
    const cur = getEdit(row.key);
    edits.set(row.key, Object.assign({}, cur, { action: actSel.value }));
    updateMoveOverwriteRiskBanner();
  });
  tdAction.appendChild(actSel);
  tr.appendChild(tdAction);

  // Conflict select
  const tdConflict = document.createElement('td');
  tdConflict.className = 'action-cell';
  const conSel = document.createElement('select');
  conSel.disabled = applying;
  for (const v of ['skip', 'overwrite']) {
    const opt = document.createElement('option');
    opt.value = v;
    opt.textContent = v;
    if (v === ed.conflict) { opt.selected = true; }
    conSel.appendChild(opt);
  }
  conSel.addEventListener('change', () => {
    const cur = getEdit(row.key);
    edits.set(row.key, Object.assign({}, cur, { conflict: conSel.value }));
    updateMoveOverwriteRiskBanner();
  });
  tdConflict.appendChild(conSel);
  tr.appendChild(tdConflict);

  // Dest name
  const tdDest = document.createElement('td');
  tdDest.className = 'dest-cell';
  const destInput = document.createElement('input');
  destInput.type = 'text';
  destInput.placeholder = row.name;
  destInput.value = ed.destName || '';
  destInput.disabled = applying;
  destInput.spellcheck = false;
  destInput.autocomplete = 'off';
  destInput.addEventListener('input', () => {
    const cur = getEdit(row.key);
    edits.set(row.key, Object.assign({}, cur, { destName: destInput.value }));
  });
  tdDest.appendChild(destInput);
  tr.appendChild(tdDest);

  // Status + error
  const tdStatus = document.createElement('td');
  const badge = buildStatusBadge(row.status);
  if (badge) { tdStatus.appendChild(badge); }
  if (row.error) {
    const err = document.createElement('div');
    err.className = 'error-text';
    err.textContent = row.error;
    tdStatus.appendChild(err);
  }
  if (Array.isArray(row.driftFieldsApplied) && row.driftFieldsApplied.length > 0) {
    const drift = document.createElement('div');
    drift.className = 'error-text';
    drift.textContent = 'drift: ' + row.driftFieldsApplied.join(', ');
    tdStatus.appendChild(drift);
  }
  tr.appendChild(tdStatus);

  return tr;
}

function render() {
  const tbody = $('rows');
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
  }

  $('f-gateway-only').checked = filter.gatewayOnly;
  $('f-available').checked = filter.available;
  $('f-drift').checked = filter.drift;
  updateMoveOverwriteRiskBanner();
  updateButtons();
}

function updateButtons() {
  const allRows = Array.from(rowMap.values());
  const anyChecked = allRows.some((r) => Boolean((edits.get(r.key) || {}).checked));
  const anyFailed = allRows.some((r) => r.status === 'error' || r.status === 'conflict');
  $('btnPreview').disabled = applying || !anyChecked;
  $('btnApply').disabled = applying || !anyChecked;
  $('btnRetry').disabled = applying || !anyFailed;
  $('btnRefresh').disabled = applying;
}

function updateMoveOverwriteRiskBanner() {
  const allRows = Array.from(rowMap.values());
  const names = [];
  for (const r of allRows) {
    const ed = edits.get(r.key) || {};
    if (ed.checked && ed.action === 'move' && ed.conflict === 'overwrite') {
      names.push(r.name);
    }
  }
  const banner = $('moveOverwriteRisk');
  if (names.length === 0) {
    banner.hidden = true;
    banner.textContent = '';
  } else {
    banner.hidden = false;
    banner.textContent = '⚠ Move + Overwrite mutates source AND discards local edits — affects: ' + names.join(', ');
  }
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

function buildPreviewEntries() {
  const allRows = Array.from(rowMap.values());
  const out = [];
  for (const r of allRows) {
    const ed = edits.get(r.key) || {};
    if (!ed.checked) { continue; }
    out.push({
      source: r.source,
      name: r.name,
      destName: (ed.destName || '').trim() || r.name,
      action: ed.action || 'copy',
      conflict: ed.conflict || 'skip',
      gatewayHasName: Boolean(r.gateway_has_name),
      driftFields: Array.isArray(r.drift_fields) ? r.drift_fields.slice() : [],
    });
  }
  return out;
}

function showPreviewModal(entries) {
  const body = $('previewBody');
  while (body.firstChild) { body.removeChild(body.firstChild); }

  // Destructive warning if ANY move-overwrite present. Reset the warn
  // element to its default (destructive) styling at the top of every
  // showPreviewModal call — otherwise the inline-style override below
  // sticks across modal opens (review finding MEDIUM-2).
  const destructive = entries.filter((e) => e.action === 'move' && e.conflict === 'overwrite').map((e) => e.name);
  const moveSourceMutators = entries.filter((e) => e.action === 'move').map((e) => e.name);
  const warn = $('previewDestructiveWarning');
  warn.textContent = '';
  warn.className = 'destructive-warning';
  warn.style.background = '';
  warn.style.color = '';
  warn.style.border = '';
  if (destructive.length > 0) {
    warn.textContent = '⚠ Move + Overwrite mutates source AND discards local edits — affects: ' + destructive.join(', ');
    warn.hidden = false;
  } else if (moveSourceMutators.length > 0) {
    warn.textContent = 'Source files will be modified by move operations: ' + moveSourceMutators.join(', ');
    warn.hidden = false;
    warn.style.background = 'var(--vscode-inputValidation-warningBackground)';
    warn.style.color = 'var(--vscode-inputValidation-warningForeground)';
    warn.style.border = '1px solid var(--vscode-inputValidation-warningBorder)';
  } else {
    warn.hidden = true;
  }

  // Table of operations
  const tbl = document.createElement('table');
  const thead = document.createElement('thead');
  const headRow = document.createElement('tr');
  for (const h of ['Source', 'Name', '→ Dest', 'Action', 'Conflict', 'Note']) {
    const th = document.createElement('th'); th.textContent = h; headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  tbl.appendChild(thead);
  const tbody = document.createElement('tbody');
  for (const e of entries) {
    const tr = document.createElement('tr');
    for (const v of [e.source, e.name, e.destName, e.action, e.conflict]) {
      const td = document.createElement('td'); td.textContent = String(v); tr.appendChild(td);
    }
    const note = document.createElement('td');
    if (e.gatewayHasName && e.conflict === 'skip') { note.textContent = 'gateway has name → skip'; note.style.color = 'var(--vscode-descriptionForeground)'; }
    else if (e.gatewayHasName && e.conflict === 'overwrite') { note.textContent = 'gateway entry will be replaced'; note.style.color = 'var(--vscode-editorWarning-foreground)'; }
    else { note.textContent = ''; }
    tr.appendChild(note);
    tbody.appendChild(tr);
  }
  tbl.appendChild(tbody);
  body.appendChild(tbl);

  $('previewModal').classList.remove('hidden');
}

function hidePreviewModal() {
  $('previewModal').classList.add('hidden');
  pendingPreviewOps = null;
}

// --- Wire toolbar ---

document.querySelectorAll('input[name="source"]').forEach((el) => {
  el.addEventListener('change', () => {
    if (!el.checked) { return; }
    currentSource = el.value;
    edits.clear(); // reset edits — different source = different rowset
    vscode.postMessage({ type: 'switchSource', source: currentSource });
  });
});

$('f-gateway-only').addEventListener('change', () => {
  filter.gatewayOnly = $('f-gateway-only').checked;
  const g = degenerateGuard(filter); filter = g.filter; $('degenerate').hidden = !g.restored; render();
});
$('f-available').addEventListener('change', () => {
  filter.available = $('f-available').checked;
  const g = degenerateGuard(filter); filter = g.filter; $('degenerate').hidden = !g.restored; render();
});
$('f-drift').addEventListener('change', () => {
  filter.drift = $('f-drift').checked;
  const g = degenerateGuard(filter); filter = g.filter; $('degenerate').hidden = !g.restored; render();
});

$('search').addEventListener('input', () => { search = $('search').value; render(); });

$('btnPreview').addEventListener('click', () => {
  if (applying) { return; }
  const entries = buildPreviewEntries();
  if (entries.length === 0) {
    showBanner('banner', 'info', 'Nothing to preview — check at least one row.');
    return;
  }
  showPreviewModal(entries);
});

$('btnApply').addEventListener('click', () => {
  if (applying) { return; }
  const entries = buildPreviewEntries();
  if (entries.length === 0) {
    showBanner('banner', 'info', 'Nothing to apply — check at least one row.');
    return;
  }
  // Apply path always shows the modal too — operator confirms the destructive
  // warning, then we post 'apply'. This ensures move+overwrite is never a
  // single-click destructive action.
  showPreviewModal(entries);
});

$('btnConfirmApply').addEventListener('click', () => {
  hidePreviewModal();
  clearBanner('banner');
  vscode.postMessage({ type: 'apply', edits: serializeEditsForHost() });
});

$('btnCancelPreview').addEventListener('click', () => {
  hidePreviewModal();
});

$('btnRetry').addEventListener('click', () => {
  if (applying) { return; }
  clearBanner('banner');
  vscode.postMessage({ type: 'retryFailed', edits: serializeEditsForHost() });
});

$('btnRefresh').addEventListener('click', () => {
  if (applying) { return; }
  vscode.postMessage({ type: 'refresh' });
});

$('btnCancel').addEventListener('click', () => {
  vscode.postMessage({ type: 'cancel' });
});

function serializeEditsForHost() {
  const out = [];
  for (const r of rowMap.values()) {
    const ed = edits.get(r.key) || {};
    out.push({
      rowKey: r.key,
      checked: Boolean(ed.checked),
      action: typeof ed.action === 'string' ? ed.action : 'copy',
      conflict: typeof ed.conflict === 'string' ? ed.conflict : 'skip',
      destName: typeof ed.destName === 'string' ? ed.destName : '',
    });
  }
  return out;
}

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
    edits = new Map(); // reset edits on full re-init
    if (typeof msg.path === 'string') {
      $('path-line').textContent = msg.path
        ? (msg.exists === false ? msg.path + ' (file does not exist)' : msg.path)
        : '';
    }
    if (typeof msg.source === 'string') {
      currentSource = msg.source;
      document.querySelectorAll('input[name="source"]').forEach((el) => {
        el.checked = (el.value === currentSource);
      });
    }
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
    const skipped = typeof msg.skipped === 'number' ? msg.skipped : 0;
    const summary = typeof msg.summary === 'string' ? msg.summary : '';
    showBanner('banner', failed === 0 ? 'ok' : 'warn',
      summary || (ok + ' applied, ' + skipped + ' skipped, ' + failed + ' failed.'));
  } else if (msg.type === 'error') {
    showBanner('banner', 'err', typeof msg.message === 'string' ? msg.message : 'An error occurred.');
  }
});

// Initial empty render
render();
</script>
</body>
</html>`;
}
