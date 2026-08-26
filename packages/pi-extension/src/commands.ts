/**
 * Slash commands for workspace status, selection, lifecycle, previews, and
 * explicit synchronization (ADR 0001). Commands are registered in every mode;
 * outside remote mode they explain that `--scrap` is required.
 */

import path from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { DEFAULT_DAEMON_URL } from "./client.ts";
import type { RemoteConfig } from "./identity.ts";
import { describeError, describeSession, type WorkspaceSession } from "./workspace.ts";

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
): void {
	const requireRemote = (ui: Ui): boolean => {
		if (!session.remoteMode) {
			ui.notify("Not in Scraps mode. Start Pi with --scrap to use a remote workspace.", "warning");
			return false;
		}
		return true;
	};

	pi.registerCommand("scrap", {
		description: "Start using a fresh remote Scraps workspace",
		handler: async (args, ctx) => {
			if (session.connectedWorkspace !== undefined) {
				ctx.ui.notify(describeSession(session), "info");
				return;
			}

			if (!session.remoteMode) {
				activateRemote({
					daemonUrl: process.env.SCRAP_DAEMON_URL ?? DEFAULT_DAEMON_URL,
					workspaceId: process.env.SCRAP_WORKSPACE_ID,
					project: process.env.SCRAP_PROJECT,
					token: process.env.SCRAP_TOKEN,
				});
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
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Connected to Scraps workspace ${workspace.id}.`, "info");
			} catch (error) {
				refreshStatus(ctx.ui);
				ctx.ui.notify(
					`Cannot start Scraps workspace: ${describeError(error)}. ` +
						"Start the daemon with `scrap up`, then run /scrap again.",
					"error",
				);
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
			if (!requireRemote(ctx.ui)) {
				return;
			}
			const id = args.trim();
			if (id.length === 0) {
				ctx.ui.notify("Usage: /scrap-select <workspace>", "warning");
				return;
			}
			try {
				const workspace = await session.connect(id);
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
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Deleted Scraps workspace ${workspace.id}.`, "info");
			} catch (error) {
				ctx.ui.notify(`Cannot delete workspace: ${describeError(error)}`, "error");
			}
		},
	});

	// M1 has no start/stop lifecycle (directory provider workspaces are
	// always running) and no preview registry yet; the commands exist so the
	// interface is stable and clearly state what is coming.

	pi.registerCommand("scrap-preview", {
		description: "List service previews exposed by the workspace",
		handler: async (_args, ctx) => {
			if (!requireRemote(ctx.ui)) {
				return;
			}
			ctx.ui.notify(
				"Service previews are not implemented yet (arrive with M7 `scrap open`).",
				"warning",
			);
		},
	});

	pi.registerCommand("scrap-sync", {
		description: "Synchronize workspace changes to the local checkout",
		handler: async (_args, ctx) => {
			if (!requireRemote(ctx.ui)) {
				return;
			}
			ctx.ui.notify(
				"Explicit synchronization is not implemented yet (ADR 0001 follow-up). " +
					"Commit inside the workspace with git and push instead.",
				"warning",
			);
		},
	});
}
