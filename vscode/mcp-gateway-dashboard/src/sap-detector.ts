import type { ServerView, ServerStatus } from './types';

/**
 * Shared locale-independent string comparator for stable cross-machine
 * ordering. Default `String.prototype.localeCompare()` honors the host
 * locale via ICU, which makes ordering non-deterministic across the user
 * base (same server list can appear in different orders on different
 * machines or even between refreshes if the runtime locale shifts).
 *
 * Fixed to `en` with `sensitivity:'variant'` + `numeric:true` so e.g.
 * `vsp-2` sorts before `vsp-10` and the order is identical everywhere.
 */
const stableCollator = new Intl.Collator('en', { sensitivity: 'variant', numeric: true });
export const compareByName = (a: string, b: string): number => stableCollator.compare(a, b);

export interface SapComponent {
	sid: string;
	client?: string;
	component: 'vsp' | 'gui';
}

export interface SapSystem {
	key: string;
	sid: string;
	client?: string;
	vsp?: ServerView;
	gui?: ServerView;
	status: ServerStatus;
	/**
	 * Phase 17.5 — true when this row was synthesized from a KeePass/credential
	 * entry and has no daemon-backed server yet. Lifecycle commands must not
	 * target imported rows.
	 */
	imported?: boolean;
}

const SAP_VSP_RE = /^vsp-([A-Z0-9]{3})(?:-(\d{3}))?$/;
const SAP_GUI_RE = /^sap-gui-([A-Z0-9]{3})(?:-(\d{3}))?$/;

export function parseSapServerName(name: string): SapComponent | null {
	let m = SAP_VSP_RE.exec(name);
	if (m) {
		return { sid: m[1], client: m[2] || undefined, component: 'vsp' };
	}
	m = SAP_GUI_RE.exec(name);
	if (m) {
		return { sid: m[1], client: m[2] || undefined, component: 'gui' };
	}
	return null;
}

export function computeSapStatus(system: SapSystem): ServerStatus {
	const vspStatus = system.vsp?.status;
	const guiStatus = system.gui?.status;

	if (!vspStatus) { return 'stopped'; }
	if (vspStatus === 'disabled') { return 'disabled'; }
	if (vspStatus === 'error') { return 'error'; }
	if (vspStatus === 'stopped') { return 'stopped'; }

	// VSP is running/starting/restarting.
	if (!guiStatus) { return vspStatus; } // GUI optional — follow VSP
	// Only degrade when VSP is stable (running). During VSP startup/restart,
	// a stopped/errored GUI is normal sequencing — not degradation.
	if (vspStatus === 'running' &&
		(guiStatus === 'error' || guiStatus === 'degraded' || guiStatus === 'stopped')) {
		return 'degraded';
	}
	return vspStatus; // Both healthy, or VSP still booting — follow VSP
}

/**
 * Phase 17.5 — Synthesize "imported" SapSystem rows for KeePass-stored
 * credentials whose names match the SAP regex but are not already present
 * in the daemon-reported set.
 *
 * These rows are informational: status = 'stopped', vsp/gui undefined.
 * contextValue='sap-imported' lets the tree view suppress lifecycle
 * actions that would fail (no daemon row to restart).
 *
 * NEVER reads secret values — only credential names.
 */
export function synthesizeKeepassSapSystems(
	credentialNames: readonly string[],
	existingKeys: ReadonlySet<string>,
): SapSystem[] {
	const byKey = new Map<string, SapSystem>();
	for (const name of credentialNames) {
		const parsed = parseSapServerName(name);
		if (!parsed) { continue; }
		const key = parsed.client ? `${parsed.sid}-${parsed.client}` : parsed.sid;
		if (existingKeys.has(key)) { continue; }
		if (byKey.has(key)) { continue; }
		byKey.set(key, {
			key,
			sid: parsed.sid,
			client: parsed.client,
			status: 'stopped',
			imported: true,
		});
	}
	const out = [...byKey.values()];
	out.sort((a, b) => compareByName(a.key, b.key));
	return out;
}

export function groupSapSystems(servers: ServerView[]): { sap: SapSystem[]; mcp: ServerView[] } {
	const sapMap = new Map<string, SapSystem>();
	const mcp: ServerView[] = [];

	for (const sv of servers) {
		const parsed = parseSapServerName(sv.name);
		if (!parsed) {
			mcp.push(sv);
			continue;
		}

		const key = parsed.client ? `${parsed.sid}-${parsed.client}` : parsed.sid;
		let system = sapMap.get(key);
		if (!system) {
			system = { key, sid: parsed.sid, client: parsed.client, status: 'stopped' };
			sapMap.set(key, system);
		}

		if (parsed.component === 'vsp') {
			system.vsp = sv;
		} else {
			system.gui = sv;
		}
	}

	// AUDIT B-NEW-27 (Phase 10): same-SID merge pass.
	// When the daemon reports e.g. `vsp-DEV` (no client) and `sap-gui-DEV-100`
	// (with client), the loop above produces two separate SapSystem entries
	// keyed "DEV" and "DEV-100", each missing one component. The user sees
	// them as unrelated rows even though they're the same SAP installation.
	// Merge rule: when a bare-SID entry shares its SID with EXACTLY ONE
	// client-bearing entry, fold the bare entry's vsp/gui into the
	// client-bearing one (the more specific row wins per PLAN_FILE B-NEW-27).
	// If multiple client variants exist for one SID, the merge is ambiguous
	// and the bare entry stays as-is.
	const bareBySid = new Map<string, string>(); // sid → bareKey
	const clientCountBySid = new Map<string, number>();
	const clientKeyBySid = new Map<string, string>(); // sid → single clientKey when count=1
	for (const system of sapMap.values()) {
		if (system.client === undefined) {
			bareBySid.set(system.sid, system.key);
		} else {
			clientCountBySid.set(system.sid, (clientCountBySid.get(system.sid) ?? 0) + 1);
			clientKeyBySid.set(system.sid, system.key);
		}
	}
	for (const [sid, bareKey] of bareBySid) {
		if (clientCountBySid.get(sid) !== 1) { continue; }
		const clientKey = clientKeyBySid.get(sid);
		if (!clientKey) { continue; }
		const bare = sapMap.get(bareKey);
		const client = sapMap.get(clientKey);
		if (!bare || !client) { continue; }
		// Conservative merge: only fold the bare entry into the client when
		// the bare contributes a component the client is currently missing
		// AND the bare itself does not have any component that overlaps with
		// the client. The "vsp-DEV is a separate transport-mgmt server next
		// to DEV-100 install" scenario must keep both rows distinct — see
		// sap-detector.test.ts:118 'groups mixed list correctly'. Merging
		// only happens for the asymmetric case the bug describes:
		//   vsp-DEV (bare) + sap-gui-DEV-100 (client, no vsp) → single row
		// or vsp-DEV-100 (client, no gui) + sap-gui-DEV (bare).
		const bareAddsVsp = bare.vsp !== undefined && client.vsp === undefined;
		const bareAddsGui = bare.gui !== undefined && client.gui === undefined;
		const bareOverlapsVsp = bare.vsp !== undefined && client.vsp !== undefined;
		const bareOverlapsGui = bare.gui !== undefined && client.gui !== undefined;
		if (!(bareAddsVsp || bareAddsGui)) { continue; } // bare has nothing new to give
		if (bareOverlapsVsp || bareOverlapsGui) { continue; } // distinct installs sharing SID
		if (bareAddsVsp) { client.vsp = bare.vsp; }
		if (bareAddsGui) { client.gui = bare.gui; }
		sapMap.delete(bareKey);
	}

	// Compute composite status for each system.
	const sap: SapSystem[] = [];
	for (const system of sapMap.values()) {
		system.status = computeSapStatus(system);
		sap.push(system);
	}

	// Sort by key / name for stable ordering across refreshes — the daemon
	// returns servers in whatever order its internal map iterates, which
	// caused the tree rows to jump on every refresh (Phase 17 follow-up).
	// `compareByName` is locale-pinned so two developers see the same order.
	sap.sort((a, b) => compareByName(a.key, b.key));
	mcp.sort((a, b) => compareByName(a.name, b.name));

	return { sap, mcp };
}
