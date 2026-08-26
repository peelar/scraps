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
 * Search: the daemon serves glob and grep endpoints (ADR 0002) with
 * server-side walking; `find` maps to files/glob, and grep is reimplemented
 * here (Pi's built-in grep spawns a local ripgrep) with output formatted
 * exactly like the built-in tool.
 */

import {
	DEFAULT_MAX_BYTES,
	type BashOperations,
	type EditOperations,
	type FindOperations,
	type LsOperations,
	type ReadOperations,
	type WriteOperations,
	formatSize,
	truncateHead,
	truncateLine,
} from "@earendil-works/pi-coding-agent";

import { type GrepInput, type GrepMatch, type WorkspaceRecord, ScrapdClient } from "./client.ts";
import { ScrapdApiError } from "./errors.ts";
import type { WorkspaceSession } from "./workspace.ts";

/** File reads: binary-safe through base64 file transport. */
export function createRemoteReadOps(session: WorkspaceSession): ReadOperations {
	return {
		readFile: (path) => remote(session, (ws, client) => client.readFile(ws.id, path)),
		access: (path) => remote(session, (ws, client) => client.access(ws.id, path, "read")),
		detectImageMimeType: async (path) => {
			const buffer = await remote(session, (ws, client) => client.readFile(ws.id, path));
			return sniffImageMime(buffer);
		},
	};
}

/** File writes: binary-safe through base64 file transport. */
export function createRemoteWriteOps(session: WorkspaceSession): WriteOperations {
	return {
		writeFile: (path, content) =>
			remote(session, (ws, client) => client.writeFile(ws.id, path, content)),
		mkdir: (dir) => remote(session, (ws, client) => client.mkdir(ws.id, dir)),
	};
}

/** Edits reuse read + write so Pi's exact-match semantics are preserved. */
export function createRemoteEditOps(session: WorkspaceSession): EditOperations {
	return {
		readFile: (path) => remote(session, (ws, client) => client.readFile(ws.id, path)),
		writeFile: (path, content) =>
			remote(session, (ws, client) => client.writeFile(ws.id, path, content)),
		access: (path) => remote(session, (ws, client) => client.access(ws.id, path, "write")),
	};
}

/**
 * Streaming command execution: output chunks, exit status, timeouts, aborts,
 * and working directory all propagate. Pi's general process environment is
 * ignored; only variables approved in the local Scraps profile are copied.
 */
export function createRemoteBashOps(
	session: WorkspaceSession,
	approvedEnv: Readonly<Record<string, string>> = {},
): BashOperations {
	return {
		exec: (command, cwd, options) =>
			remote(session, async (ws, client) => {
				try {
					return await client.exec(ws.id, command, cwd, {
						onData: options.onData,
						...(Object.keys(approvedEnv).length === 0 ? {} : { env: approvedEnv }),
						...(options.signal === undefined ? {} : { signal: options.signal }),
						...(options.timeout === undefined ? {} : { timeout: options.timeout }),
					});
				} finally {
					// Commands may leave a server listening; offer a preview hint.
					session.schedulePortsCheck();
				}
			}),
	};
}

export function createRemoteLsOps(session: WorkspaceSession): LsOperations {
	return {
		exists: (path) =>
			remote(session, async (ws, client) => {
				try {
					return (await client.stat(ws.id, path)).exists;
				} catch (error) {
					if (error instanceof ScrapdApiError && error.status === 404) {
						return false;
					}
					throw error;
				}
			}),
		stat: (path) =>
			remote(session, async (ws, client) => {
				const entry = await client.stat(ws.id, path);
				return { isDirectory: () => entry.isDirectory };
			}),
		readdir: (path) => remote(session, (ws, client) => client.readdir(ws.id, path)),
	};
}

export function createRemoteFindOps(session: WorkspaceSession): FindOperations {
	return {
		exists: (path) =>
			remote(session, async (ws, client) => {
				try {
					return (await client.stat(ws.id, path)).exists;
				} catch (error) {
					if (error instanceof ScrapdApiError && error.status === 404) {
						return false;
					}
					throw error;
				}
			}),
		// The daemon returns workspace-relative paths. Convert them back to
		// stable agent-visible paths for Pi's built-in find renderer.
		glob: (pattern, cwd, options) =>
			remote(session, async (ws, client) => {
				const paths = await client.glob(ws.id, {
					pattern,
					path: cwd,
					...(options.limit > 0 ? { limit: options.limit } : {}),
				});
				return paths.map((path) => displayPath(path, session.root));
			}),
	};
}

export type RemoteGrepDetails = {
	truncation?: ReturnType<typeof truncateHead>;
	matchLimitReached?: number;
	linesTruncated?: boolean;
};

export type RemoteGrepOutput = {
	readonly text: string;
	readonly details: RemoteGrepDetails | undefined;
};

const DEFAULT_GREP_LIMIT = 100;

/**
 * Remote grep via the daemon's search endpoint, formatted exactly like Pi's
 * built-in tool: `path:line: text` for matches, `path-line- text` for
 * context, plus the built-in truncation notices.
 */
export async function runRemoteGrep(
	session: WorkspaceSession,
	input: GrepInput,
): Promise<RemoteGrepOutput> {
	const ws = session.requireWorkspace();
	const client = session.requireClient();
	const result = await client.grep(ws.id, {
		...input,
		// Resolve like Pi's other path-taking tools; the client translates the
		// stable virtual path to the workspace-relative API contract.
		path: resolveRemotePath(input.path, session.root),
	});

	if (result.matches.length === 0) {
		return { text: "No matches found", details: undefined };
	}

	const effectiveLimit = input.limit ?? DEFAULT_GREP_LIMIT;
	let linesTruncated = false;
	const outputLines: string[] = [];

	for (const match of result.matches) {
		const path = relativeToRoot(match.path, session.root);
		if ((input.context ?? 0) === 0) {
			const sanitized = match.lineText.replace(/\r\n/g, "\n").replace(/\r/g, "").replace(/\n$/, "");
			const { text, wasTruncated } = truncateLine(sanitized);
			if (wasTruncated) {
				linesTruncated = true;
			}
			outputLines.push(`${path}:${match.lineNumber}: ${text}`);
		} else if (match.lines.length > 0) {
			for (const line of match.lines) {
				const { text, wasTruncated } = truncateLine(line.text.replace(/\r/g, ""));
				if (wasTruncated) {
					linesTruncated = true;
				}
				outputLines.push(
					line.match ? `${path}:${line.n}: ${text}` : `${path}-${line.n}- ${text}`,
				);
			}
		} else {
			outputLines.push(`${path}:${match.lineNumber}: ${match.lineText}`);
		}
	}

	// Byte truncation only: the match limit already capped rows.
	const truncation = truncateHead(outputLines.join("\n"), {
		maxLines: Number.MAX_SAFE_INTEGER,
	});

	const notices: string[] = [];
	const details: RemoteGrepDetails = {};
	if (result.limitReached) {
		notices.push(
			`${effectiveLimit} matches limit reached. Use limit=${effectiveLimit * 2} for more, or refine pattern`,
		);
		details.matchLimitReached = effectiveLimit;
	}
	if (truncation.truncated) {
		notices.push(`${formatSize(DEFAULT_MAX_BYTES)} limit reached`);
		details.truncation = truncation;
	}
	if (linesTruncated) {
		notices.push("Some lines truncated. Use read tool to see full lines");
		details.linesTruncated = true;
	}

	const text =
		notices.length > 0 ? `${truncation.content}\n\n[${notices.join(". ")}]` : truncation.content;
	return { text, details: Object.keys(details).length > 0 ? details : undefined };
}

/** Render a workspace-relative API path under the stable virtual root. */
export function displayPath(path: string, root: string): string {
	if (path === "" || path === ".") return root;
	if (path.startsWith("/")) return path;
	return `${root}/${path.replace(/^\.\//, "")}`;
}

/** Render a workspace path relative to the project root. */
export function relativeToRoot(path: string, root: string): string {
	const normalizedRoot = root.endsWith("/") ? root : `${root}/`;
	if (path.startsWith(normalizedRoot)) {
		return path.slice(normalizedRoot.length);
	}
	return path;
}

/** Resolve a tool path argument (possibly relative or ".") against the root. */
export function resolveRemotePath(path: string | undefined, root: string): string {
	if (path === undefined || path === "" || path === ".") {
		return root;
	}
	if (path.startsWith("/")) {
		return path;
	}
	return `${root}/${path}`;
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

async function remote<T>(
	session: WorkspaceSession,
	operation: (ws: WorkspaceRecord, client: ScrapdClient) => Promise<T>,
): Promise<T> {
	return operation(session.requireWorkspace(), session.requireClient());
}

export type { GrepInput, GrepMatch };
