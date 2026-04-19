#!/usr/bin/env node
/*
 * check-catalog-refs.js — verify every commands.json entry references a
 * server.name that exists in servers.json. Run as part of `npm run lint:catalog`.
 *
 * Plain JavaScript per CV-gate D-2: keeping this off the TypeScript build path
 * means CI never has to run a TS executor (no ts-node bootstrap, no tsc step).
 */

'use strict';

const fs = require('node:fs');
const path = require('node:path');

function checkCatalogRefs(servers, commands) {
	if (!Array.isArray(servers)) {
		return { valid: false, errors: ['servers.json is not an array'] };
	}
	if (!Array.isArray(commands)) {
		return { valid: false, errors: ['commands.json is not an array'] };
	}
	const serverNames = new Set();
	for (const s of servers) {
		if (s && typeof s === 'object' && typeof s.name === 'string') {
			serverNames.add(s.name);
		}
	}
	const errors = [];
	for (const cmd of commands) {
		if (!cmd || typeof cmd !== 'object') {
			errors.push('commands.json: encountered non-object entry');
			continue;
		}
		if (typeof cmd.server_name !== 'string') {
			errors.push(`commands.json: ${cmd.command_name || '(unknown)'} has non-string server_name`);
			continue;
		}
		if (!serverNames.has(cmd.server_name)) {
			errors.push(
				`commands.json: ${cmd.command_name || '(unknown)'} references unknown server_name '${cmd.server_name}'`,
			);
		}
	}
	return { valid: errors.length === 0, errors };
}

function readJsonOrExit(filePath) {
	try {
		return JSON.parse(fs.readFileSync(filePath, 'utf8'));
	} catch (err) {
		process.stderr.write(`check-catalog-refs: failed to read ${filePath}: ${err.message}\n`);
		process.exit(2);
	}
}

function main(argv) {
	// Default paths are relative to repo root when invoked as
	// `npm run lint:catalog` (cwd = vscode/mcp-gateway-dashboard).
	const serversPath = argv[2] || path.join('docs', 'catalog', 'servers.json');
	const commandsPath = argv[3] || path.join('docs', 'catalog', 'commands.json');

	const servers = readJsonOrExit(serversPath);
	const commands = readJsonOrExit(commandsPath);
	const result = checkCatalogRefs(servers, commands);
	if (!result.valid) {
		for (const e of result.errors) {
			process.stderr.write(`check-catalog-refs: ${e}\n`);
		}
		process.exit(1);
	}
	process.stdout.write(
		`check-catalog-refs: OK — ${commands.length} command(s) reference ${servers.length} server(s)\n`,
	);
}

if (require.main === module) {
	main(process.argv);
}

module.exports = { checkCatalogRefs };
