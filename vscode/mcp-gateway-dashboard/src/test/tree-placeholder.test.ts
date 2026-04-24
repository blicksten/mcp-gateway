import './mock-vscode';

import * as assert from 'node:assert';
import { describe, it } from 'mocha';
import { PlaceholderTreeItem } from '../tree-placeholder';

describe('PlaceholderTreeItem', () => {
	it('uses the "Connecting to gateway…" label', () => {
		const item = new PlaceholderTreeItem();
		assert.strictEqual(item.label, 'Connecting to gateway…');
	});

	it('has collapsibleState=None (no children)', () => {
		const item = new PlaceholderTreeItem();
		// MockTreeItem stores TreeItemCollapsibleState.None as 0.
		assert.strictEqual(item.collapsibleState, 0);
	});

	it('uses distinct contextValue=gateway-connecting', () => {
		// Distinct context value keeps the placeholder out of every per-server
		// menu `when` clause. Any change to this string must be matched in
		// package.json to avoid silently enabling actions on the placeholder.
		const item = new PlaceholderTreeItem();
		assert.strictEqual(item.contextValue, 'gateway-connecting');
	});

	it('uses sync~spin theme icon', () => {
		const item = new PlaceholderTreeItem();
		const icon = item.iconPath as { id: string };
		assert.strictEqual(icon.id, 'sync~spin');
	});

	it('sets a tooltip string', () => {
		const item = new PlaceholderTreeItem();
		assert.ok(typeof item.tooltip === 'string' && item.tooltip.length > 0);
	});
});
