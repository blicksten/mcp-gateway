/** Server name validation: alphanumeric, hyphens, underscores, max 64 chars (matches Go serverNameRe). */
export const SERVER_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/;

/** Environment variable key: POSIX identifier. */
export const ENV_KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** HTTP header name: RFC 7230 token. Regex is unavoidable — the RFC token grammar
 *  is a character class over non-contiguous punctuation that cannot be expressed
 *  with string methods without loss of fidelity. */
export const HEADER_NAME_RE = /^[!#$%&'*+\-.^_`|~A-Za-z0-9]+$/;

/**
 * Platform-agnostic absolute-path check. Recognizes:
 *  - POSIX absolute paths starting with "/"
 *  - Windows drive-letter paths (`C:\...` or `C:/...`)
 *  - UNC paths (`\\host\share`)
 *
 * Uses string methods only (per project rules — regex is last resort). We do NOT
 * call Node's `path.isAbsolute`, because it is platform-sensitive: on Linux it
 * rejects `C:\bin`, which would cause a confusing client-side / server-side
 * validation asymmetry for Windows users running against a Linux extension host
 * or CI environment. The rule here is pure-syntactic and matches the JS version
 * embedded in the Add Server webview.
 */
export function isAbsolutePath(p: string): boolean {
	const s = p.trim();
	if (s.length === 0) { return false; }
	// POSIX absolute.
	if (s.charAt(0) === '/') { return true; }
	// UNC (\\host\share).
	if (s.length >= 2 && s.charAt(0) === '\\' && s.charAt(1) === '\\') { return true; }
	// Windows drive letter: C:\... or C:/...
	if (s.length >= 3) {
		const c = s.charCodeAt(0);
		const isLetter = (c >= 65 && c <= 90) || (c >= 97 && c <= 122);
		if (isLetter && s.charAt(1) === ':' && (s.charAt(2) === '\\' || s.charAt(2) === '/')) {
			return true;
		}
	}
	return false;
}

export function validateServerName(v: string): string | null {
	if (!v.trim()) { return 'Name is required'; }
	if (!SERVER_NAME_RE.test(v.trim())) {
		return 'Name must start with a letter/digit, contain only letters, digits, hyphens, or underscores, and be at most 64 characters';
	}
	return null;
}

export function validateUrl(v: string): string | null {
	if (!v.trim()) { return 'URL is required'; }
	try {
		const parsed = new URL(v.trim());
		if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
			return 'URL must use http: or https: scheme';
		}
	} catch {
		return 'Invalid URL format';
	}
	return null;
}

/** Validate stdio command: must be a non-empty, absolute path (platform-agnostic). */
export function validateStdioCommand(v: string): string | null {
	if (!v.trim()) { return 'Command is required'; }
	if (!isAbsolutePath(v.trim())) {
		return 'Use an absolute path — run "where <cmd>" to find it';
	}
	return null;
}

/** Validate env var KEY=VALUE entry. */
export function validateEnvEntry(v: string): string | null {
	const trimmed = v.trim();
	if (!trimmed) { return null; }
	const eq = trimmed.indexOf('=');
	if (eq < 1) { return 'Must be KEY=VALUE format'; }
	const key = trimmed.substring(0, eq);
	if (!ENV_KEY_RE.test(key)) {
		return 'Key must be a valid identifier (letters, digits, underscores)';
	}
	return null;
}

/** Validate HTTP header "Name: Value" entry. */
export function validateHeaderEntry(v: string): string | null {
	const trimmed = v.trim();
	if (!trimmed) { return null; }
	const colon = trimmed.indexOf(':');
	if (colon < 1) { return 'Must be Name: Value format'; }
	const hName = trimmed.substring(0, colon).trim();
	if (!HEADER_NAME_RE.test(hName)) {
		return 'Header name must be a valid RFC 7230 token';
	}
	return null;
}

/** Detect transport from user-provided URL-or-command string. */
export function detectTransport(urlOrCommand: string): 'http' | 'stdio' {
	const trimmed = urlOrCommand.trim();
	if (trimmed.startsWith('http://') || trimmed.startsWith('https://')) {
		return 'http';
	}
	return 'stdio';
}

export interface ParsedEnvEntry { key: string; value: string }
export interface ParsedHeaderEntry { name: string; value: string }

/** Parse a KEY=VALUE entry. Caller must validate first with {@link validateEnvEntry}. */
export function parseEnvEntry(v: string): ParsedEnvEntry | null {
	const trimmed = v.trim();
	const eq = trimmed.indexOf('=');
	if (eq < 1) { return null; }
	return { key: trimmed.substring(0, eq), value: trimmed.substring(eq + 1) };
}

/** Parse a "Name: Value" entry. Caller must validate first with {@link validateHeaderEntry}. */
export function parseHeaderEntry(v: string): ParsedHeaderEntry | null {
	const trimmed = v.trim();
	const colon = trimmed.indexOf(':');
	if (colon < 1) { return null; }
	return { name: trimmed.substring(0, colon).trim(), value: trimmed.substring(colon + 1).trim() };
}
