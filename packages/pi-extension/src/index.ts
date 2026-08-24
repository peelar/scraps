/**
 * Scraps Pi extension (ADR 0001).
 *
 * Runs Pi locally with all project-facing tools (bash, read, write, edit,
 * ls, find, grep) executing in a remote Scraps workspace controlled by the
 * scrapd daemon. The extension is dormant unless Pi is started with the
 * explicit `--scrap` flag:
 *
 *   pi --scrap
 *   pi --scrap --workspace <workspace>
 *
 * The `scrap pi` CLI is a convenience launcher that resolves or creates a
 * workspace and starts local Pi with this extension and workspace identity
 * (SCRAP_DAEMON_URL, SCRAP_WORKSPACE_ID, SCRAP_PROJECT, SCRAP_TOKEN).
 *
 * While remote mode is active the extension fails closed: without a verified
 * workspace connection every replaced tool reports a visible error and never
 * falls back to the developer machine.
 */

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { registerScrapCommands } from "./commands.ts";
import { resolveMode } from "./identity.ts";
import { createRemoteBashOps } from "./operations.ts";
import { adaptSystemPrompt } from "./prompt.ts";
import { statusText } from "./workspace.ts";
import { registerRemoteTools } from "./tools.ts";
import { WorkspaceSession, describeError } from "./workspace.ts";

const STATUS_KEY = "scrap";

export default function scrapsExtension(pi: ExtensionAPI): void {
	pi.registerFlag("scrap", {
		description: "Run project tools in a remote Scraps workspace (fail-closed)",
		type: "boolean",
		default: false,
	});
	pi.registerFlag("workspace", {
		description: "Scraps workspace id to attach to (overrides SCRAP_WORKSPACE_ID)",
		type: "string",
	});

	const session = new WorkspaceSession();
	let toolsRegistered = false;

	const refreshStatus = (ui: { setStatus: (key: string, text: string | undefined) => void }) => {
		ui.setStatus(STATUS_KEY, statusText(session));
	};

	registerScrapCommands(pi, session, refreshStatus);

	pi.on("session_start", async (_event, ctx) => {
		// CLI flags are not available during the factory; resolve identity now.
		const mode = resolveMode({
			flags: {
				scrap: pi.getFlag("scrap"),
				workspace: pi.getFlag("workspace"),
			},
			env: process.env,
		});

		if (mode.kind === "local") {
			ctx.ui.setStatus(STATUS_KEY, undefined);
			return;
		}

		session.configure(mode.config);
		if (!toolsRegistered) {
			registerRemoteTools(pi, session);
			toolsRegistered = true;
		}

		if (ctx.hasUI && mode.config.daemonUrl === undefined) {
			ctx.ui.notify(
				"--scrap is active but SCRAP_DAEMON_URL is not set. Remote tools will fail " +
					"closed until a daemon is configured.",
				"error",
			);
		}

		if (mode.config.workspaceId !== undefined && mode.config.daemonUrl !== undefined) {
			try {
				const workspace = await session.connect();
				if (ctx.hasUI) {
					ctx.ui.notify(
						`Connected to Scraps workspace ${workspace.id} (${workspace.status}).`,
						"info",
					);
				}
			} catch (error) {
				// Fail closed, loudly: tools stay remote and will error until
				// the user reconnects.
				if (ctx.hasUI) {
					ctx.ui.notify(
						`Cannot connect to Scraps workspace ${mode.config.workspaceId}: ` +
							describeError(error),
						"error",
					);
				}
			}
		}

		refreshStatus(ctx.ui);
	});

	pi.on("before_agent_start", async (event, ctx) => {
		if (!session.remoteMode) {
			return;
		}
		const workspace = session.connectedWorkspace;
		const daemonUrl = session.daemonUrl ?? "not configured";
		const localCwd = event.systemPromptOptions?.cwd ?? process.cwd();
		return {
			systemPrompt: adaptSystemPrompt(event.systemPrompt, localCwd, {
				workspaceId: session.workspaceId ?? "none",
				project: workspace?.project ?? session.project,
				daemonUrl,
				root: session.root,
				status: workspace?.status ?? "disconnected",
			}),
		};
	});

	// Route `!` and `!!` shell commands to the workspace as well; project-
	// affecting execution must not silently hit the local machine either.
	pi.on("user_bash", (_event) => {
		if (!session.remoteMode) {
			return undefined;
		}
		return { operations: createRemoteBashOps(session) };
	});
}
