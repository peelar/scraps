/**
 * Remote tool registration.
 *
 * Replaces Pi's seven project-facing tools (bash, read, write, edit, ls,
 * find, grep) with remote-workspace implementations (ADR 0001). Registration
 * occurs when `/scrap`, `/scrap-select`, or the compatibility startup flag
 * activates remote mode; before that Pi's built-ins remain untouched.
 *
 * read/write/edit/bash/ls/find reuse Pi's own tool factories with remote
 * operations, so result shapes, rendering, truncation, and exact-match edit
 * behavior stay identical to the local tools. grep cannot reuse the built-in
 * execute path (it spawns a local ripgrep), so it runs `rg` inside the
 * workspace through the streaming exec endpoint.
 *
 * While remote mode is active there is no local fallback: a disconnected
 * workspace fails closed. After `/scrap toss` deletes the workspace, these
 * same-name overrides delegate back to Pi's local implementations.
 */

import {
	createBashTool,
	createEditTool,
	createFindTool,
	createGrepTool,
	createLsTool,
	createReadTool,
	createWriteTool,
	type ExtensionAPI,
} from "@earendil-works/pi-coding-agent";

import {
	createRemoteBashOps,
	createRemoteEditOps,
	createRemoteFindOps,
	createRemoteLsOps,
	createRemoteReadOps,
	createRemoteWriteOps,
	runRemoteGrep,
} from "./operations.ts";
import type { WorkspaceSession } from "./workspace.ts";

export const REMOTE_TOOL_NAMES = [
	"bash",
	"read",
	"write",
	"edit",
	"ls",
	"find",
	"grep",
] as const;

/**
 * Register the seven remote tools. Tools are (re)built per execution so a
 * mid-session `/scrap-select` or late workspace discovery is picked up
 * without re-registering anything.
 */
export function registerRemoteTools(
	pi: ExtensionAPI,
	session: WorkspaceSession,
	approvedEnv: Readonly<Record<string, string>> = {},
): void {
	// Tools are (re)built per execution so a mid-session `/scrap-select` or
	// late workspace discovery is picked up without re-registering anything.
	const readTool = (cwd: string) => session.remoteMode
		? createReadTool(session.root, { operations: createRemoteReadOps(session) })
		: createReadTool(cwd);
	const writeTool = (cwd: string) => session.remoteMode
		? createWriteTool(session.root, { operations: createRemoteWriteOps(session) })
		: createWriteTool(cwd);
	const editTool = (cwd: string) => session.remoteMode
		? createEditTool(session.root, { operations: createRemoteEditOps(session) })
		: createEditTool(cwd);
	const bashTool = (cwd: string) => session.remoteMode
		? createBashTool(session.root, { operations: createRemoteBashOps(session, approvedEnv) })
		: createBashTool(cwd);
	const lsTool = (cwd: string) => session.remoteMode
		? createLsTool(session.root, { operations: createRemoteLsOps(session) })
		: createLsTool(cwd);
	const findTool = (cwd: string) => session.remoteMode
		? createFindTool(session.root, { operations: createRemoteFindOps(session) })
		: createFindTool(cwd);
	const metadataCwd = session.root;

	// Same-name registration overrides the built-in tool; metadata (schema,
	// description, renderers) comes from Pi's own factory.
	pi.registerTool({
		...readTool(metadataCwd),
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			return readTool(ctx.cwd).execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...writeTool(metadataCwd),
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			return writeTool(ctx.cwd).execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...editTool(metadataCwd),
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			return editTool(ctx.cwd).execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...bashTool(metadataCwd),
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			return bashTool(ctx.cwd).execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...lsTool(metadataCwd),
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			return lsTool(ctx.cwd).execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...findTool(metadataCwd),
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			return findTool(ctx.cwd).execute(toolCallId, params, signal, onUpdate);
		},
	});

	registerRemoteGrep(pi, session);
}

function registerRemoteGrep(pi: ExtensionAPI, session: WorkspaceSession): void {
	// Reuse the built-in schema and metadata so the model-facing contract and
	// renderer inheritance stay identical; only execution is remote.
	type GrepTool = ReturnType<typeof createGrepTool>;
	const meta: GrepTool = createGrepTool(session.root);
	pi.registerTool({
		...meta,
		async execute(toolCallId, params, signal, onUpdate, ctx) {
			if (!session.remoteMode) {
				return createGrepTool(ctx.cwd).execute(toolCallId, params, signal, onUpdate);
			}
			const input = params as {
				pattern: string;
				path?: string;
				glob?: string;
				ignoreCase?: boolean;
				literal?: boolean;
				context?: number;
				limit?: number;
			};
			const result = await runRemoteGrep(session, input);
			return {
				content: [{ type: "text" as const, text: result.text }],
				details: result.details,
			};
		},
	});
}
