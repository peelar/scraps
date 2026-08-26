/**
 * Slash commands for workspace status, selection, lifecycle, previews, and
 * explicit synchronization (ADR 0001). Commands are registered in every mode;
 * `/scrap` and `/scrap-select` can activate remote mode from ordinary Pi.
 */

import path from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { DEFAULT_DAEMON_URL } from "./client.ts";
import { clientEnvironment } from "./config.ts";
import type { RemoteConfig, SessionBinding } from "./identity.ts";
import { describeError, describeSession, portsHint, type WorkspaceSession } from "./workspace.ts";

/** Minimal UI surface the commands need (satisfied by ExtensionContext.ui). */
type Ui = {
	notify: (message: string, level: "info" | "warning" | "error") => void;
	setStatus: (key: string, text: string | undefined) => void;
};

export function registerScrapCommands(
	pi: ExtensionAPI,
	session: WorkspaceSession,
	refreshStatus: (ui: Ui) => void,
	activateRemote: (config: RemoteConfig) => void,
	persistBinding: (binding: SessionBinding) => void,
	notifyEnvironment: (ui: Ui) => void,
): void {
	const defaultRemoteConfig = (): RemoteConfig => {
		const env = clientEnvironment();
		return {
			daemonUrl: env.SCRAP_DAEMON_URL ?? DEFAULT_DAEMON_URL,
			workspaceId: env.SCRAP_WORKSPACE_ID,
			project: env.SCRAP_PROJECT,
			token: env.SCRAP_TOKEN,
		};
	};
	const persistRemote = () => {
		const project = session.connectedWorkspace?.project ?? session.project;
		persistBinding({
			version: 1,
			mode: "remote",
			...(session.daemonUrl === undefined ? {} : { daemonUrl: session.daemonUrl }),
			...(session.workspaceId === undefined ? {} : { workspaceId: session.workspaceId }),
			...(project === undefined ? {} : { project }),
		});
	};
	const requireRemote = (ui: Ui): boolean => {
		if (!session.remoteMode) {
			ui.notify("Not in Scraps mode. Run /scrap or /scrap-select to attach a workspace.", "warning");
			return false;
		}
		return true;
	};

	/** Preview hints: notify when a workspace port starts listening. */
	const wirePortsNotifier = (ui: Ui): void => {
		session.setPortsListener((ports) => {
			ui.notify(portsHint(ports), "info");
			refreshStatus(ui);
		});
	};

	pi.registerCommand("scrap", {
		description: "Attach a remote workspace, or toss it and return local",
		handler: async (args, ctx) => {
			if (args.trim() === "toss") {
				const workspace = session.connectedWorkspace;
				if (workspace === undefined) {
					ctx.ui.notify("No connected Scraps workspace to toss.", "warning");
					return;
				}
				const confirmed = await ctx.ui.confirm(
					"Toss workspace?",
					`Permanently delete ${workspace.id} and all uncommitted work, then return Pi local?`,
				);
				if (!confirmed) return;
				try {
					await session.remove();
					session.deactivate();
					persistBinding({ version: 1, mode: "local" });
					refreshStatus(ctx.ui);
					ctx.ui.notify(`Tossed ${workspace.id}. Project tools and !/!! now run locally.`, "warning");
				} catch (error) {
					refreshStatus(ctx.ui);
					ctx.ui.notify(
						`Cannot toss ${workspace.id}: ${describeError(error)}. Still using the remote workspace.`,
						"error",
					);
				}
				return;
			}
			if (session.connectedWorkspace !== undefined) {
				ctx.ui.notify(describeSession(session), "info");
				return;
			}

			if (!session.remoteMode) {
				activateRemote(defaultRemoteConfig());
				notifyEnvironment(ctx.ui);
				persistRemote();
				refreshStatus(ctx.ui);
			}

			try {
				const configuredWorkspace = session.workspaceId;
				const workspace =
					configuredWorkspace === undefined
						? await session.create(
								args.trim() || session.project || path.basename(ctx.cwd) || "workspace",
							)
						: await session.connect(configuredWorkspace);
				wirePortsNotifier(ctx.ui);
				persistRemote();
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Connected to Scraps workspace ${workspace.id}.`, "info");
			} catch (error) {
				refreshStatus(ctx.ui);
				const local = session.daemonUrl === undefined || session.daemonUrl === DEFAULT_DAEMON_URL;
				const hint = local
					? "Start the local worker VM with `make up`, or attach a remote worker with `scrap attach`, then run /scrap again."
					: `Check the worker with \`scrap status\` (${session.daemonUrl}); if it moved, re-attach with \`scrap attach\`.`;
				ctx.ui.notify(`Cannot start Scraps workspace: ${describeError(error)}. ${hint}`, "error");
			}
		},
	});

	pi.registerCommand("scrap-list", {
		description: "List Scraps workspaces",
		handler: async (_args, ctx) => {
			if (!requireRemote(ctx.ui)) {
				return;
			}
			try {
				const workspaces = await session.list();
				if (workspaces.length === 0) {
					ctx.ui.notify("No Scraps workspaces exist yet.", "info");
					return;
				}
				const lines = workspaces.map(
					(workspace) => `${workspace.id}  ${workspace.state}  ${workspace.project ?? ""}`,
				);
				ctx.ui.notify(lines.join("\n"), "info");
			} catch (error) {
				ctx.ui.notify(`Cannot list workspaces: ${describeError(error)}`, "error");
			}
		},
	});

	pi.registerCommand("scrap-select", {
		description: "Attach this Pi session to an existing Scraps workspace",
		handler: async (args, ctx) => {
			const id = args.trim();
			if (id.length === 0) {
				ctx.ui.notify("Usage: /scrap-select <workspace>", "warning");
				return;
			}
			if (!session.remoteMode) {
				activateRemote({ ...defaultRemoteConfig(), workspaceId: id });
				notifyEnvironment(ctx.ui);
				persistRemote();
				refreshStatus(ctx.ui);
			}
			try {
				const workspace = await session.connect(id);
				wirePortsNotifier(ctx.ui);
				persistRemote();
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Connected to Scraps workspace ${workspace.id}.`, "info");
			} catch (error) {
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Cannot attach to ${id}: ${describeError(error)}`, "error");
			}
		},
	});

	pi.registerCommand("scrap-new", {
		description: "Create a new Scraps workspace and attach to it",
		handler: async (args, ctx) => {
			if (!requireRemote(ctx.ui)) {
				return;
			}
			const project = args.trim();
			try {
				const workspace = await session.create(project.length > 0 ? project : undefined);
				wirePortsNotifier(ctx.ui);
				persistRemote();
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Created Scraps workspace ${workspace.id}.`, "info");
			} catch (error) {
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Cannot create workspace: ${describeError(error)}`, "error");
			}
		},
	});

	pi.registerCommand("scrap-rm", {
		description: "Delete the attached Scraps workspace",
		handler: async (_args, ctx) => {
			if (!requireRemote(ctx.ui)) {
				return;
			}
			const workspace = session.connectedWorkspace;
			if (workspace === undefined) {
				ctx.ui.notify("No workspace is attached. Use /scrap-select first.", "warning");
				return;
			}
			const ok = await ctx.ui.confirm(
				"Delete workspace?",
				`This permanently deletes ${workspace.id} and all uncommitted changes.`,
			);
			if (!ok) {
				return;
			}
			try {
				await session.remove();
				persistRemote();
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Deleted Scraps workspace ${workspace.id}.`, "info");
			} catch (error) {
				ctx.ui.notify(`Cannot delete workspace: ${describeError(error)}`, "error");
			}
		},
	});
}
