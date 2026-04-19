import { promises as fsp } from 'node:fs';
import * as path from 'node:path';
import * as vscode from 'vscode';

/**
 * Resolve the catalog directory: operator override (`mcpGateway.catalogPath`)
 * wins when non-empty AND the path is an existing directory; otherwise fall
 * back to the bundled `<extensionPath>/docs/catalog/` directory (CB.4 + CC.1).
 *
 * Shared helper so AddServerPanel (CB) and SlashCommandGenerator (CC) apply
 * identical resolution rules — keeping this in one place prevents drift
 * between the two code paths if the D2/D4 decisions ever evolve.
 *
 * Returns `null` when no usable directory can be resolved — the caller then
 * surfaces the loader's standard "no path provided" warning rather than a
 * silent relative-path read.
 */
export async function resolveCatalogDir(
	extensionUri: vscode.Uri | undefined,
): Promise<string | null> {
	// `get<string>` is a type-cast hint, not a runtime validator. An operator
	// with a corrupted settings.json or a policy-managed override could feed a
	// non-string through this path, and `.trim()` on a non-string throws
	// TypeError inside a fire-and-forget promise. Guard with an explicit
	// runtime `typeof` check (CB.GATE Round 2, Sonnet 4.6 CB-1 finding).
	const rawCatalogPath = vscode.workspace.getConfiguration('mcpGateway')
		.get('catalogPath', '');
	const operator = (typeof rawCatalogPath === 'string' ? rawCatalogPath : '').trim();
	if (operator) {
		try {
			const st = await fsp.stat(operator);
			if (st.isDirectory()) { return operator; }
		} catch {
			// fall through to bundled path
		}
	}
	// extensionUri.fsPath is the OS-native filesystem path of the extension
	// install directory. In tests the mock passes a plain object with fsPath
	// pointing at a temp dir. The `.path` fallback exists only for legacy
	// mock shapes — production vscode.Uri always populates fsPath.
	if (!extensionUri) { return null; }
	const fsPath = (extensionUri as { fsPath?: string }).fsPath
		?? (extensionUri as { path?: string }).path;
	if (!fsPath) { return null; }
	return path.join(fsPath, 'docs', 'catalog');
}
