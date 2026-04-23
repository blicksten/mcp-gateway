// Mock vscode module for unit tests that run outside VS Code (mocha + ts-node).
// Must be imported BEFORE any module that imports 'vscode'.

import Module from 'node:module';

// Mock MarkdownString — matches the subset of vscode.MarkdownString used by the
// extension. `value` accumulates appended markdown, `isTrusted` guards command
// links, `supportHtml` mirrors the VS Code API field. Exported so tests can
// type-narrow tooltips set by production code.
export class MockMarkdownString {
	value: string;
	isTrusted: boolean;
	supportHtml: boolean;

	constructor(value?: string, supportThemeIcons: boolean = false) {
		void supportThemeIcons;
		this.value = value ?? '';
		this.isTrusted = false;
		this.supportHtml = false;
	}

	appendMarkdown(value: string): this {
		this.value += value;
		return this;
	}

	appendText(value: string): this {
		this.value += value;
		return this;
	}

	appendCodeblock(value: string, language?: string): this {
		this.value += `\n\`\`\`${language ?? ''}\n${value}\n\`\`\`\n`;
		return this;
	}
}

class MockTreeItem {
	label: string;
	collapsibleState: number;
	contextValue?: string;
	description?: string;
	tooltip?: string | MockMarkdownString;
	iconPath?: unknown;
	command?: unknown;

	constructor(label: string, collapsibleState: number = 0) {
		this.label = label;
		this.collapsibleState = collapsibleState;
	}
}

class MockEventEmitter<T> {
	private handlers: Array<(e: T) => void> = [];

	get event(): (handler: (e: T) => void) => { dispose(): void } {
		return (handler: (e: T) => void) => {
			this.handlers.push(handler);
			return { dispose: () => { this.handlers = this.handlers.filter((h) => h !== handler); } };
		};
	}

	fire(data: T): void {
		for (const h of this.handlers) {
			h(data);
		}
	}

	dispose(): void {
		this.handlers = [];
	}
}

class MockThemeIcon {
	constructor(public readonly id: string, public readonly color?: MockThemeColor) {}
}

class MockThemeColor {
	constructor(public readonly id: string) {}
}

const TreeItemCollapsibleState = { None: 0, Collapsed: 1, Expanded: 2 };
const StatusBarAlignment = { Left: 1, Right: 2 };

// Mock StatusBarItem — tracks state for assertions.
export interface MockStatusBarItem {
	text: string;
	tooltip: string | MockMarkdownString | undefined;
	command: string | undefined;
	backgroundColor: { id: string } | undefined;
	color: { id: string } | undefined;
	visible: boolean;
	disposed: boolean;
	show(): void;
	hide(): void;
	dispose(): void;
}

// Mock WebviewPanel — tracks state for assertions.
export interface MockWebview {
	html: string;
	cspSource: string;
	onDidReceiveMessage: (handler: (msg: unknown) => void) => { dispose(): void };
	postMessage: (msg: unknown) => Promise<boolean>;
	_messageHandlers: Array<(msg: unknown) => void>;
	_simulateMessage: (msg: unknown) => void;
}

export interface MockWebviewPanel {
	webview: MockWebview;
	viewType: string;
	title: string;
	visible: boolean;
	disposed: boolean;
	reveal(): void;
	dispose(): void;
	onDidDispose: (handler: () => void) => { dispose(): void };
	_disposeHandlers: Array<() => void>;
	_postedMessages: unknown[];
}

export const mockWebviewPanels: MockWebviewPanel[] = [];

// Mock WebviewView — sidebar variant used by WebviewViewProvider.
export interface MockWebviewView {
	webview: MockWebview;
	visible: boolean;
	disposed: boolean;
	dispose(): void;
	onDidDispose: (handler: () => void) => { dispose(): void };
	_disposeHandlers: Array<() => void>;
}

export function createMockWebviewView(): MockWebviewView {
	const messageHandlers: Array<(msg: unknown) => void> = [];
	const disposeHandlers: Array<() => void> = [];
	const postedMessages: unknown[] = [];

	const webview: MockWebview & { options?: unknown } = {
		html: '',
		cspSource: '${webview.cspSource}',
		onDidReceiveMessage: (handler: (msg: unknown) => void) => {
			messageHandlers.push(handler);
			return { dispose: () => { const i = messageHandlers.indexOf(handler); if (i >= 0) { messageHandlers.splice(i, 1); } } };
		},
		postMessage: async (msg: unknown) => { postedMessages.push(msg); return true; },
		_messageHandlers: messageHandlers,
		_simulateMessage: (msg: unknown) => { for (const h of messageHandlers) { h(msg); } },
	};

	const view: MockWebviewView = {
		webview,
		visible: true,
		disposed: false,
		dispose() {
			view.disposed = true;
			for (const h of disposeHandlers) { h(); }
		},
		onDidDispose: (handler: () => void) => {
			disposeHandlers.push(handler);
			return { dispose: () => { const i = disposeHandlers.indexOf(handler); if (i >= 0) { disposeHandlers.splice(i, 1); } } };
		},
		_disposeHandlers: disposeHandlers,
	};
	return view;
}

// Mock OutputChannel — tracks appended lines for assertions.
export interface MockOutputChannel {
	name: string;
	lines: string[];
	disposed: boolean;
	appendLine(line: string): void;
	append(text: string): void;
	clear(): void;
	show(): void;
	hide(): void;
	dispose(): void;
}

export const mockOutputChannels: MockOutputChannel[] = [];

// Exported for test assertions.
export const mockStatusBarItems: MockStatusBarItem[] = [];

// Configurable workspace.getConfiguration values: tests can set/clear entries
// in `mockConfigValues` to exercise production code paths that depend on
// specific settings (e.g. `mcpGateway.sapGroupBySid`).
export const mockConfigValues: Record<string, unknown> = {};

// Registered onDidChangeConfiguration handlers — tests use
// `fireConfigChange(key)` to simulate a live setting change from the UI.
const configChangeHandlers: Array<(e: { affectsConfiguration: (key: string) => boolean }) => void> = [];

/** Fire a synthetic configuration-change event to all registered handlers. */
export function fireConfigChange(changedKey: string): void {
	const evt = { affectsConfiguration: (k: string) => k === changedKey };
	for (const h of [...configChangeHandlers]) { h(evt); }
}

// Registered commands store — accessible from tests for invocation.
const registeredCommands = new Map<string, (...args: unknown[]) => unknown>();

// Configurable dialog responses for tests.
// For sequential responses (e.g. addServer flow), use inputBoxQueue/quickPickQueue.
export const dialogResponses: {
	showInformationMessage?: unknown;
	showWarningMessage?: unknown;
	showErrorMessage?: unknown;
	showInputBox?: string | undefined;
	showQuickPick?: unknown;
	inputBoxQueue: Array<string | undefined>;
	quickPickQueue: unknown[];
} = { inputBoxQueue: [], quickPickQueue: [] };

// Track calls for assertion.
export const mockCalls: {
	clipboard: string[];
	errorMessages: string[];
	infoMessages: string[];
	warningMessages: string[];
} = {
	clipboard: [],
	errorMessages: [],
	infoMessages: [],
	warningMessages: [],
};

// Mock SecretStorage — in-memory Map for credential tests.
export class MockSecretStorage {
	private _data = new Map<string, string>();

	async get(key: string): Promise<string | undefined> {
		return this._data.get(key);
	}

	async store(key: string, value: string): Promise<void> {
		this._data.set(key, value);
	}

	async delete(key: string): Promise<void> {
		this._data.delete(key);
	}

	keys(): string[] {
		return [...this._data.keys()];
	}

	clear(): void {
		this._data.clear();
	}
}

// Mock Memento (globalState) — in-memory for tests.
export class MockMemento {
	private data = new Map<string, unknown>();

	get<T>(key: string, defaultValue?: T): T | undefined {
		if (this.data.has(key)) { return this.data.get(key) as T; }
		return defaultValue;
	}

	async update(key: string, value: unknown): Promise<void> {
		this.data.set(key, value);
	}

	keys(): readonly string[] {
		return [...this.data.keys()];
	}

	clear(): void {
		this.data.clear();
	}
}

export const dispatchedCommands: Array<{ id: string; args: unknown[] }> = [];

export function resetMockState(): void {
	registeredCommands.clear();
	dispatchedCommands.length = 0;
	dialogResponses.showInformationMessage = undefined;
	dialogResponses.showWarningMessage = undefined;
	dialogResponses.showErrorMessage = undefined;
	dialogResponses.showInputBox = undefined;
	dialogResponses.showQuickPick = undefined;
	dialogResponses.inputBoxQueue = [];
	dialogResponses.quickPickQueue = [];
	mockCalls.clipboard = [];
	mockCalls.errorMessages = [];
	mockCalls.infoMessages = [];
	mockCalls.warningMessages = [];
	mockStatusBarItems.length = 0;
	mockOutputChannels.length = 0;
	mockWebviewPanels.length = 0;
	for (const k of Object.keys(mockConfigValues)) { delete mockConfigValues[k]; }
	configChangeHandlers.length = 0;
}

export function getRegisteredCommands(): Map<string, (...args: unknown[]) => unknown> {
	return registeredCommands;
}

const ViewColumn = { One: 1, Two: 2, Three: 3 };

class MockUri {
	readonly scheme: string;
	readonly path: string;
	constructor(scheme: string, path: string) { this.scheme = scheme; this.path = path; }
	static file(p: string): MockUri { return new MockUri('file', p); }
	with(_change: Record<string, unknown>): MockUri { return this; }
	toString(): string { return `${this.scheme}://${this.path}`; }
}

export const mockVscode = {
	TreeItem: MockTreeItem,
	TreeItemCollapsibleState,
	StatusBarAlignment,
	ViewColumn,
	Uri: MockUri,
	EventEmitter: MockEventEmitter,
	ThemeIcon: MockThemeIcon,
	ThemeColor: MockThemeColor,
	MarkdownString: MockMarkdownString,
	commands: {
		registerCommand: (id: string, handler: (...args: unknown[]) => unknown) => {
			registeredCommands.set(id, handler);
			return { dispose: () => { registeredCommands.delete(id); } };
		},
		executeCommand: async (id: string, ...args: unknown[]) => {
			dispatchedCommands.push({ id, args });
			const handler = registeredCommands.get(id);
			if (handler) { return handler(...args); }
		},
	},
	window: {
		createTreeView: () => ({
			dispose: () => {},
			onDidChangeSelection: (_handler: (e: unknown) => void) => ({ dispose: () => {} }),
		}),
		registerWebviewViewProvider: (_viewType: string, _provider: unknown) => ({ dispose: () => {} }),
		createWebviewPanel: (viewType: string, title: string, _showOptions?: unknown, _options?: unknown): MockWebviewPanel => {
			const messageHandlers: Array<(msg: unknown) => void> = [];
			const disposeHandlers: Array<() => void> = [];
			const postedMessages: unknown[] = [];

			const webview: MockWebview = {
				html: '',
				cspSource: '${webview.cspSource}',
				onDidReceiveMessage: (handler: (msg: unknown) => void) => {
					messageHandlers.push(handler);
					return { dispose: () => { const i = messageHandlers.indexOf(handler); if (i >= 0) { messageHandlers.splice(i, 1); } } };
				},
				postMessage: async (msg: unknown) => { postedMessages.push(msg); return true; },
				_messageHandlers: messageHandlers,
				_simulateMessage: (msg: unknown) => { for (const h of messageHandlers) { h(msg); } },
			};

			const panel: MockWebviewPanel = {
				webview,
				viewType,
				title,
				visible: true,
				disposed: false,
				reveal() { panel.visible = true; },
				dispose() {
					panel.disposed = true;
					for (const h of disposeHandlers) { h(); }
				},
				onDidDispose: (handler: () => void) => {
					disposeHandlers.push(handler);
					return { dispose: () => { const i = disposeHandlers.indexOf(handler); if (i >= 0) { disposeHandlers.splice(i, 1); } } };
				},
				_disposeHandlers: disposeHandlers,
				_postedMessages: postedMessages,
			};

			mockWebviewPanels.push(panel);
			return panel;
		},
		createOutputChannel: (name: string): MockOutputChannel => {
			const ch: MockOutputChannel = {
				name,
				lines: [],
				disposed: false,
				appendLine(line: string) { if (!ch.disposed) { ch.lines.push(line); } },
				append(text: string) { if (!ch.disposed) { ch.lines.push(text); } },
				clear() { ch.lines.length = 0; },
				show() {},
				hide() {},
				dispose() { ch.disposed = true; },
			};
			mockOutputChannels.push(ch);
			return ch;
		},
		createStatusBarItem: (_alignment?: number, _priority?: number): MockStatusBarItem => {
			const item: MockStatusBarItem = {
				text: '',
				tooltip: undefined,
				command: undefined,
				backgroundColor: undefined,
				color: undefined,
				visible: false,
				disposed: false,
				show() { item.visible = true; },
				hide() { item.visible = false; },
				dispose() { item.disposed = true; },
			};
			mockStatusBarItems.push(item);
			return item;
		},
		showInformationMessage: (...args: unknown[]) => {
			mockCalls.infoMessages.push(String(args[0]));
			return Promise.resolve(dialogResponses.showInformationMessage);
		},
		showWarningMessage: (...args: unknown[]) => {
			mockCalls.warningMessages.push(String(args[0]));
			return Promise.resolve(dialogResponses.showWarningMessage);
		},
		showErrorMessage: (...args: unknown[]) => {
			mockCalls.errorMessages.push(String(args[0]));
			return Promise.resolve(dialogResponses.showErrorMessage);
		},
		showInputBox: () => {
			if (dialogResponses.inputBoxQueue.length > 0) {
				return Promise.resolve(dialogResponses.inputBoxQueue.shift());
			}
			return Promise.resolve(dialogResponses.showInputBox);
		},
		showQuickPick: (items: unknown[]) => {
			if (dialogResponses.quickPickQueue.length > 0) {
				return Promise.resolve(dialogResponses.quickPickQueue.shift());
			}
			return Promise.resolve(dialogResponses.showQuickPick ?? items[0]);
		},
	},
	workspace: {
		getConfiguration: (section?: string) => ({
			get: (key: string, defaultValue: unknown) => {
				// Match either the fully-qualified key (`${section}.${key}`)
				// or the bare key — tests may set either form.
				const fullKey = section ? `${section}.${key}` : key;
				if (fullKey in mockConfigValues) { return mockConfigValues[fullKey]; }
				if (key in mockConfigValues) { return mockConfigValues[key]; }
				return defaultValue;
			},
		}),
		onDidChangeConfiguration: (handler: (e: { affectsConfiguration: (key: string) => boolean }) => void) => {
			configChangeHandlers.push(handler);
			return { dispose: () => {
				const i = configChangeHandlers.indexOf(handler);
				if (i >= 0) { configChangeHandlers.splice(i, 1); }
			} };
		},
		workspaceFolders: undefined as unknown[] | undefined,
	},
	env: {
		clipboard: {
			writeText: (text: string) => {
				mockCalls.clipboard.push(text);
				return Promise.resolve();
			},
		},
	},
};

// Intercept require('vscode') to return our mock.
// NOTE: This patch applies process-wide and is never reverted. All test files in
// this mocha process share the same vscode mock. Use resetMockState() between tests.
const originalRequire = Module.prototype.require;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
(Module.prototype as any).require = function (this: NodeModule, id: string) {
	if (id === 'vscode') {
		return mockVscode;
	}
	return originalRequire.call(this, id);
};
