/**
 * Remote tool registration.
 *
 * Replaces Pi's seven project-facing tools (bash, read, write, edit, ls,
 * find, grep) with remote-workspace implementations (ADR 0001). Registration
 * happens only while remote mode is active; without `--scrap` the extension
 * leaves Pi's built-in tools completely untouched.
 *
 * read/write/edit/bash/ls/find reuse Pi's own tool factories with remote
 * operations, so result shapes, rendering, truncation, and exact-match edit
 * behavior stay identical to the local tools. grep cannot reuse the built-in
 * execute path (it spawns a local ripgrep), so it runs `rg` inside the
 * workspace through the streaming exec endpoint.
 *
 * There is no local fallback anywhere in this module: when the workspace is
 * not connected, the operations layer throws and the tool call fails
 * visibly.
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
export function registerRemoteTools(pi: ExtensionAPI, session: WorkspaceSession): void {
	// Tools are (re)built per execution so a mid-session `/scrap-select` or
	// late workspace discovery is picked up without re-registering anything.
	const readTool = () => createReadTool(session.root, { operations: createRemoteReadOps(session) });
	const writeTool = () => createWriteTool(session.root, { operations: createRemoteWriteOps(session) });
	const editTool = () => createEditTool(session.root, { operations: createRemoteEditOps(session) });
	const bashTool = () => createBashTool(session.root, { operations: createRemoteBashOps(session) });
	const lsTool = () => createLsTool(session.root, { operations: createRemoteLsOps(session) });
	const findTool = () => createFindTool(session.root, { operations: createRemoteFindOps(session) });

	// Same-name registration overrides the built-in tool; metadata (schema,
	// description, renderers) comes from Pi's own factory.
	pi.registerTool({
		...readTool(),
		async execute(toolCallId, params, signal, onUpdate) {
			return readTool().execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...writeTool(),
		async execute(toolCallId, params, signal, onUpdate) {
			return writeTool().execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...editTool(),
		async execute(toolCallId, params, signal, onUpdate) {
			return editTool().execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...bashTool(),
		async execute(toolCallId, params, signal, onUpdate) {
			return bashTool().execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...lsTool(),
		async execute(toolCallId, params, signal, onUpdate) {
			return lsTool().execute(toolCallId, params, signal, onUpdate);
		},
	});
	pi.registerTool({
		...findTool(),
		async execute(toolCallId, params, signal, onUpdate) {
			return findTool().execute(toolCallId, params, signal, onUpdate);
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
		async execute(_toolCallId, params) {
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
			const details: { matchLimitReached?: number } = {};
			if (result.matchLimitReached !== undefined) {
				details.matchLimitReached = result.matchLimitReached;
			}
			return {
				content: [{ type: "text" as const, text: result.text }],
				details,
			};
		},
	});
}
