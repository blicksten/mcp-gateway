import * as vscode from 'vscode';
import type { SapSystem } from './sap-detector';
import type { ServerStatus, ServerView } from './types';
import { escapeMd } from './markdown-utils';

const STATUS_ICONS: Record<ServerStatus, string> = {
	running: 'vm-running',
	stopped: 'debug-stop',
	error: 'error',
	degraded: 'warning',
	disabled: 'circle-slash',
	starting: 'loading~spin',
	restarting: 'sync~spin',
};

const STATUS_DOTS: Record<ServerStatus, string> = {
	running: '\u25CF',      // ●
	stopped: '\u25CB',      // ○
	error: '\u2716',        // ✖
	degraded: '\u26A0',     // ⚠
	disabled: '\u2298',     // ⊘
	starting: '\u25CB',
	restarting: '\u25CB',
};

/**
 * SapSystemItem represents a SAP system row. In flat mode (default) it shows
 * composite VSP/GUI status inline via description dots. In hierarchical mode
 * (when `mcpGateway.sapGroupBySid` is on), it becomes a collapsible parent
 * whose children are {@link SapComponentItem} rows.
 */
export class SapSystemItem extends vscode.TreeItem {
	readonly system: SapSystem;
	readonly hierarchical: boolean;

	constructor(system: SapSystem, hierarchical: boolean = false) {
		super(
			system.key,
			// Imported rows never expand — there are no daemon-backed components.
			hierarchical && !system.imported
				? vscode.TreeItemCollapsibleState.Collapsed
				: vscode.TreeItemCollapsibleState.None,
		);
		this.system = system;
		this.hierarchical = hierarchical;

		// Icon based on composite status.
		const iconId = system.imported
			? 'cloud-download'
			: (STATUS_ICONS[system.status] ?? 'question');
		this.iconPath = new vscode.ThemeIcon(iconId);

		// Description: component status dots (kept in hierarchical mode as a
		// quick glance even when the children are collapsed).
		if (system.imported) {
			this.description = 'imported (KeePass)';
		} else {
			const parts: string[] = [];
			if (system.vsp) {
				parts.push(`vsp ${STATUS_DOTS[system.vsp.status] ?? '?'}`);
			}
			if (system.gui) {
				parts.push(`gui ${STATUS_DOTS[system.gui.status] ?? '?'}`);
			}
			this.description = parts.join('  ');
		}

		// Tooltip with details (MarkdownString for rich rendering).
		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;
		md.appendMarkdown(`**SAP System:** ${escapeMd(system.sid)}\n\n`);
		if (system.client) { md.appendMarkdown(`- Client: \`${escapeMd(system.client)}\`\n`); }
		if (system.imported) {
			md.appendMarkdown(`- Source: KeePass-imported credential\n`);
			md.appendMarkdown(`- Status: not running (no daemon-backed server)\n\n`);
			md.appendMarkdown(`_Use **Add SAP System** to register this system with the daemon._\n`);
		} else {
			if (system.vsp) {
				md.appendMarkdown(`- VSP: \`${escapeMd(system.vsp.name)}\` (${system.vsp.status})\n`);
				if (system.vsp.pid) { md.appendMarkdown(`  - PID: \`${system.vsp.pid}\`\n`); }
				if (system.vsp.restart_count > 0) { md.appendMarkdown(`  - Restarts: ${system.vsp.restart_count}\n`); }
				if (system.vsp.last_error) { md.appendMarkdown(`  - Error: ${escapeMd(system.vsp.last_error)}\n`); }
			}
			if (system.gui) {
				md.appendMarkdown(`- GUI: \`${escapeMd(system.gui.name)}\` (${system.gui.status})\n`);
				if (system.gui.pid) { md.appendMarkdown(`  - PID: \`${system.gui.pid}\`\n`); }
				if (system.gui.restart_count > 0) { md.appendMarkdown(`  - Restarts: ${system.gui.restart_count}\n`); }
				if (system.gui.last_error) { md.appendMarkdown(`  - Error: ${escapeMd(system.gui.last_error)}\n`); }
			}
		}
		this.tooltip = md;

		// contextValue:
		// - `sap-imported` — Phase 17.5 synthetic row, no daemon component;
		//   package.json when-clauses exclude lifecycle actions on this tag.
		// - `sap-group-<status>` — hierarchical parent of a daemon-backed system.
		// - `sap-<status>` — flat-mode daemon-backed row.
		if (system.imported) {
			this.contextValue = 'sap-imported';
		} else {
			this.contextValue = hierarchical
				? `sap-group-${system.status}`
				: `sap-${system.status}`;
		}
	}
}

/**
 * SapComponentItem represents one component (VSP or GUI) of a SAP system when
 * the tree is in hierarchical mode. Exposes the underlying ServerView so
 * existing restartSapVsp/restartSapGui + log commands work unchanged.
 */
export class SapComponentItem extends vscode.TreeItem {
	readonly system: SapSystem;
	readonly kind: 'vsp' | 'gui';
	readonly server: ServerView;

	constructor(system: SapSystem, kind: 'vsp' | 'gui', server: ServerView) {
		super(kind.toUpperCase(), vscode.TreeItemCollapsibleState.None);
		this.system = system;
		this.kind = kind;
		this.server = server;

		const iconId = STATUS_ICONS[server.status] ?? 'question';
		this.iconPath = new vscode.ThemeIcon(iconId);

		this.description = server.status;

		const md = new vscode.MarkdownString();
		md.isTrusted = false;
		md.supportHtml = false;
		md.appendMarkdown(`**${kind.toUpperCase()}:** \`${escapeMd(server.name)}\`\n\n`);
		md.appendMarkdown(`- Status: ${server.status}\n`);
		if (server.transport) { md.appendMarkdown(`- Transport: \`${escapeMd(server.transport)}\`\n`); }
		if (server.pid !== undefined && server.pid !== null) { md.appendMarkdown(`- PID: \`${server.pid}\`\n`); }
		if (server.restart_count > 0) { md.appendMarkdown(`- Restarts: ${server.restart_count}\n`); }
		if (server.last_error) { md.appendMarkdown(`- Error: ${escapeMd(server.last_error)}\n`); }
		this.tooltip = md;

		// sap-vsp-<status> / sap-gui-<status> — package.json when-clauses gate
		// per-component actions on these patterns.
		this.contextValue = `sap-${kind}-${server.status}`;
	}
}

