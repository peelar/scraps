/**
 * Scraps Pi extension (ADR 0001).
 *
 * Runs Pi locally with all project-facing tools (bash, read, write, edit,
 * ls, find, grep) executing in a remote Scraps workspace controlled by the
 * scrapd daemon. In ordinary Pi, `/scrap` activates remote mode and creates a
 * workspace; `/scrap-select` attaches an existing one. `--scrap` remains an
 * internal compatibility flag, not the public launcher UX.
 *
 * While remote mode is active the extension fails closed: without a verified
 * workspace connection every replaced tool reports a visible error and never
 * falls back to the developer machine.
 */

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { fileURLToPath } from "node:url";

import { registerScrapCommands } from "./commands.ts";
import { approvedCommandEnvironmentState, clientEnvironment } from "./config.ts";
import {
	SESSION_BINDING_ENTRY,
	resolveSessionMode,
	restoreSessionBinding,
	type SessionBinding,
} from "./identity.ts";
import { createRemoteBashOps } from "./operations.ts";
import { adaptSystemPrompt } from "./prompt.ts";
import { registerDurableRuns } from "./runs.ts";
import { portsHint, statusText } from "./workspace.ts";
import { REMOTE_TOOL_NAMES, registerRemoteTools } from "./tools.ts";
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
	const environmentState = approvedCommandEnvironmentState();
	const approvedEnv = environmentState.values;
	let toolsRegistered = false;
	let environmentNoticeShown = false;

	const notifyEnvironment = (ui: { notify: (message: string, level: "info" | "warning" | "error") => void }) => {
		if (environmentNoticeShown) return;
		environmentNoticeShown = true;
		if (environmentState.missing.length > 0) {
			ui.notify(
				`Scraps: approved environment ${environmentState.missing.join(", ")} ${environmentState.missing.length === 1 ? "was" : "were"} not set when Pi started. Restart Pi through your secret manager to load ${environmentState.missing.length === 1 ? "it" : "them"}.`,
				"warning",
			);
			return;
		}
		if (environmentState.loaded.length > 0) {
			ui.notify(`Scraps: loaded approved environment: ${environmentState.loaded.join(", ")}.`, "info");
			return;
		}
		ui.notify(
			"Scraps isolates your local environment by default. If software needs a variable, the agent will guide you through approving it safely.",
			"info",
		);
	};

	const refreshStatus = (ui: { setStatus: (key: string, text: string | undefined) => void }) => {
		ui.setStatus(STATUS_KEY, statusText(session));
	};

	const activateRemote = (config: Parameters<WorkspaceSession["configure"]>[0]) => {
		session.configure(config);
		if (!toolsRegistered) {
			registerRemoteTools(pi, session, approvedEnv);
			const active = pi.getActiveTools();
			pi.setActiveTools([...new Set([...active, ...REMOTE_TOOL_NAMES])]);
			toolsRegistered = true;
		}
	};

	const persistBinding = (binding: SessionBinding) => {
		pi.appendEntry(SESSION_BINDING_ENTRY, binding);
	};

	registerScrapCommands(pi, session, refreshStatus, activateRemote, persistBinding, notifyEnvironment);

	// Ship the Scraps skill with the extension so Pi can guide setup (worker
	// discovery, `scrap attach`, workspace lifecycle) without a source checkout.
	pi.on("resources_discover", async () => ({
		skillPaths: [fileURLToPath(new URL("./skills", import.meta.url))],
	}));

	pi.on("session_start", async (_event, ctx) => {
		// CLI flags are not available during the factory; resolve identity now.
		const binding = restoreSessionBinding(ctx.sessionManager.getBranch());
		const mode = resolveSessionMode(
			{
				flags: {
					scrap: pi.getFlag("scrap"),
					workspace: pi.getFlag("workspace"),
				},
				env: clientEnvironment(),
			},
			binding,
		);

		if (mode.kind === "local") {
			ctx.ui.setStatus(STATUS_KEY, undefined);
			return;
		}

		activateRemote(mode.config);
		notifyEnvironment(ctx.ui);

		// Preview hints: notify when a workspace port starts listening.
		session.setPortsListener((ports) => {
			if (!ctx.hasUI) {
				return;
			}
			ctx.ui.notify(portsHint(ports), "info");
			refreshStatus(ctx.ui);
		});

		if (mode.config.workspaceId !== undefined) {
			try {
				const workspace = await session.connect();
				if (ctx.hasUI) {
					ctx.ui.notify(
						`Connected to Scraps workspace ${workspace.id} (${workspace.state}).`,
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
				status: workspace?.state ?? "disconnected",
				loadedEnvironment: environmentState.loaded,
				missingEnvironment: environmentState.missing,
			}),
		};
	});

	// Route `!` and `!!` shell commands to the workspace as well; project-
	// affecting execution must not silently hit the local machine either.
	pi.on("user_bash", (_event) => {
		if (!session.remoteMode) {
			return undefined;
		}
		return { operations: createRemoteBashOps(session, approvedEnv) };
	});

	// The worker owns the complete agent loop in Scraps mode. The ordinary
	// local TUI becomes a reconnectable client while `/scrap` remains the only
	// user-facing activation command; missing runner support fails closed.
	registerDurableRuns(pi, session);
}
