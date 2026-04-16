import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

export function createTmpDir(): string {
	return fs.mkdtempSync(path.join(os.tmpdir(), 'mcp-gw-test-'));
}

export function cleanupTmpDir(dirPath: string): void {
	fs.rmSync(dirPath, { recursive: true, force: true });
}
