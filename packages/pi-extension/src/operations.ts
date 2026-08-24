/**
 * Remote tool operations.
 *
 * Implements Pi's pluggable operations interfaces (read, write, edit, bash,
 * ls, find) on top of the scrapd client so Pi's own tool implementations keep
 * their exact result shapes, rendering, truncation, and exact-match edit
 * behavior (ADR 0001: "provide binary-safe file operations and preserve Pi's
 * exact-match edit behavior").
 *
 * Every operation goes through `session.requireWorkspace()`, the fail-closed
 * gate: with no connected workspace the operation throws and the tool reports
 * a visible error. Nothing ever falls back to the local machine.
 *
 * Search tools: the built-in `grep` spawns a local ripgrep process, so remote
 * grep runs `rg` inside the workspace through the streaming exec endpoint
 * instead. `find` uses the custom `glob` seam the same way with `fd`. When
 * scrapd grows dedicated search endpoints these can move off exec without
 * touching the tool layer.
 */

import {
	type BashOperations,
	type EditOperations,
	type FindOperations,
	type LsOperations,
	type ReadOperations,
	type WriteOperations,
} from "@earendil-works/pi-coding-agent";

import { ScrapdApiError } from "./errors.ts";
import type { WorkspaceSession } from "./workspace.ts";

export type RemoteGrepInput = {
	readonly pattern: string;
	readonly path?: string;
	readonly glob?: string;
	readonly ignoreCase?: boolean;
	readonly literal?: boolean;
	readonly context?: number;
	readonly limit?: number;
};

export type RemoteGrepResult = {
	readonly text: string;
	readonly matchLimitReached?: number;
};

/** Single-quote a string for a POSIX shell on the workspace. */
export function shQuote(value: string): string {
	return `'${value.replaceAll("'", `'\\''`)}'`;
}

function sniffImageMime(buffer: Buffer): string | null {
	if (buffer.length >= 3 && buffer[0] === 0xff && buffer[0x1] === 0xd8 && buffer[0x2] === 0xff) {
		return "image/jpeg";
	}
	if (
		buffer.length >= 8 &&
		buffer[0] === 0x89 &&
		buffer[0x1] === 0x50 &&
		buffer[0x2] === 0x4e &&
		buffer[0x3] === 0x47
	) {
		return "image/png";
	}
	if (buffer.length >= 6) {
		const header = buffer.subarray(0, 6).toString("latin1");
		if (header === "GIF87a" || header === "GIF89a") {
			return "image/gif";
		}
	}
	if (buffer.length >= 12) {
		const riff = buffer.subarray(0, 4).toString("latin1");
		const webp = buffer.subarray(8, 12).toString("latin1");
		if (riff === "RIFF" && webp === "WEBP") {
			return "image/webp";
		}
	}
	return null;
}

/** File reads: binary-safe through base64 file transport. */
export function createRemoteReadOps(session: WorkspaceSession): ReadOperations {
	return {
		readFile: (path) => execOrFail(session, (ws, client) => client.readFile(ws.id, path)),
		access: (path) => execOrFail(session, (ws, client) => client.access(ws.id, path, "r")),
		detectImageMimeType: async (path) => {
			const ws = session.requireWorkspace();
			const buffer = await session.requireClient().readFile(ws.id, path);
			return sniffImageMime(buffer);
		},
	};
}

/** File writes: binary-safe through base64 file transport. */
export function createRemoteWriteOps(session: WorkspaceSession): WriteOperations {
	return {
		writeFile: (path, content) =>
			execOrFail(session, (ws, client) => client.writeFile(ws.id, path, content)),
		mkdir: (dir) => execOrFail(session, (ws, client) => client.mkdir(ws.id, dir)),
	};
}

/** Edits reuse read + write so Pi's exact-match semantics are preserved. */
export function createRemoteEditOps(session: WorkspaceSession): EditOperations {
	return {
		readFile: (path) => execOrFail(session, (ws, client) => client.readFile(ws.id, path)),
		writeFile: (path, content) =>
			execOrFail(session, (ws, client) => client.writeFile(ws.id, path, content)),
		access: (path) => execOrFail(session, (ws, client) => client.access(ws.id, path, "rw")),
	};
}

/**
 * Streaming command execution: output chunks, exit status, timeouts, aborts,
 * working directory, and environment additions all propagate (ADR 0001).
 */
export function createRemoteBashOps(session: WorkspaceSession): BashOperations {
	return {
		exec: (command, cwd, options) =>
			execOrFail(session, (ws, client) =>
				client.exec(ws.id, command, cwd, {
					onData: options.onData,
					signal: options.signal,
					timeout: options.timeout,
					env: options.env,
				}),
			),
	};
}

export function createRemoteLsOps(session: WorkspaceSession): LsOperations {
	return {
		exists: (path) =>
			execOrFail(session, async (ws, client) => {
				try {
					await client.stat(ws.id, path);
					return true;
				} catch (error) {
					if (error instanceof ScrapdApiError && error.status === 404) {
						return false;
					}
					throw error;
				}
			}),
		stat: (path) =>
			execOrFail(session, async (ws, client) => {
				const entry = await client.stat(ws.id, path);
				return { isDirectory: () => entry.directory };
			}),
		readdir: (path) => execOrFail(session, (ws, client) => client.readdir(ws.id, path)),
	};
}

export function createRemoteFindOps(session: WorkspaceSession): FindOperations {
	return {
		exists: (path) =>
			execOrFail(session, async (ws, client) => {
				try {
					await client.stat(ws.id, path);
					return true;
				} catch (error) {
					if (error instanceof ScrapdApiError && error.status === 404) {
						return false;
					}
					throw error;
				}
			}),
		glob: (pattern, cwd, options) =>
			execOrFail(session, async (ws, client) => {
				const args = ["fd", "--glob", "--color=never", "--hidden", "--type", "f"];
				for (const ignore of options.ignore) {
					args.push("--exclude", shQuote(ignore));
				}
				if (options.limit > 0) {
					args.push("--max-results", String(options.limit));
				}
				args.push(shQuote(pattern), ".");

				let output = "";
				const result = await client.exec(ws.id, args.join(" "), cwd, {
					onData: (chunk) => {
						output += chunk.toString("utf8");
					},
				});
				if (result.exitCode !== 0) {
					throw new Error(`remote fd failed with exit code ${result.exitCode}`);
				}
				return output
					.split("\n")
					.map((line) => line.trim())
					.filter((line) => line.length > 0);
			}),
	};
}

/** Build the ripgrep command for a remote grep invocation. */
export function buildGrepCommand(input: RemoteGrepInput): string {
	const args = ["rg", "--line-number", "--no-heading", "--with-filename", "--color", "never"];
	if (input.ignoreCase === true) {
		args.push("--ignore-case");
	}
	if (input.literal === true) {
		args.push("--fixed-strings");
	}
	if (input.context !== undefined) {
		args.push("--context", String(input.context));
	}
	if (input.glob !== undefined) {
		args.push("--glob", shQuote(input.glob));
	}
	args.push("--", shQuote(input.pattern), input.path === undefined ? "." : shQuote(input.path));
	return args.join(" ");
}

/**
 * Remote grep: run ripgrep inside the workspace and return
 * `path:line:content` output, honoring the match limit like the built-in.
 */
export async function runRemoteGrep(
	session: WorkspaceSession,
	input: RemoteGrepInput,
): Promise<RemoteGrepResult> {
	const ws = session.requireWorkspace();
	const client = session.requireClient();

	let output = "";
	const result = await client.exec(ws.id, buildGrepCommand(input), session.root, {
		onData: (chunk) => {
			output += chunk.toString("utf8");
		},
	});

	// ripgrep exits 1 for "no matches", 2 for real errors.
	if (result.exitCode !== 0 && result.exitCode !== 1) {
		throw new Error(`remote rg failed with exit code ${result.exitCode}`);
	}

	const limit = input.limit ?? 100;
	const lines = output.split("\n").filter((line) => line.length > 0);
	if (lines.length > limit) {
		return {
			text: lines.slice(0, limit).join("\n"),
			matchLimitReached: limit,
		};
	}
	return { text: lines.join("\n") };
}

async function execOrFail<T>(
	session: WorkspaceSession,
	operation: (ws: ReturnType<WorkspaceSession["requireWorkspace"]>, client: ReturnType<WorkspaceSession["requireClient"]>) => Promise<T>,
): Promise<T> {
	return operation(session.requireWorkspace(), session.requireClient());
}
