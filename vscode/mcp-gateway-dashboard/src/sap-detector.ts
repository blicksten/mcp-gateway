import type { ServerView, ServerStatus } from './types';

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

	// Compute composite status for each system.
	const sap: SapSystem[] = [];
	for (const system of sapMap.values()) {
		system.status = computeSapStatus(system);
		sap.push(system);
	}

	// Sort by key for stable ordering.
	sap.sort((a, b) => a.key.localeCompare(b.key));

	return { sap, mcp };
}
