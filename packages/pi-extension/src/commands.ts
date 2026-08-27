/**
 * Slash commands for workspace status, selection, lifecycle, previews, and
 * explicit synchronization (ADR 0001). Commands are registered in every mode;
 * `/scrap` and `/scrap-select` can activate remote mode from ordinary Pi.
 */

import { execFile as execFileCallback, spawn } from "node:child_process";
import { readdir } from "node:fs/promises";
import path from "node:path";
import { Readable } from "node:stream";
import { promisify } from "node:util";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { DEFAULT_DAEMON_URL } from "./client.ts";
import { clientEnvironment } from "./config.ts";
import { ScrapdApiError } from "./errors.ts";
import type { RemoteConfig, SessionBinding } from "./identity.ts";
import { describeError, describeSession, portsHint, type WorkspaceSession } from "./workspace.ts";

const execFile = promisify(execFileCallback);

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
	detectRemote: (cwd: string) => Promise<string | undefined> = detectGitRemoteUrl,
	inspectLocalDirectory: (cwd: string) => Promise<number> = countLocalEntries,
	buildLocalArchive: (cwd: string) => Promise<ReadableStream<Uint8Array>> = tarLocalDirectory,
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
				const creating = configuredWorkspace === undefined;
				// Workspaces never receive local files directly (ADR 0001); a git
				// remote is the transport, so offer the local checkout's origin.
				const repoUrl = creating ? await offerRepositoryClone(ctx, detectRemote) : undefined;
				// Without a repository, offer a one-shot directory copy (ADR 0014)
				// so scratch projects can enter the workspace without a forge.
				const pushDirectory = creating && repoUrl === undefined
					? await offerDirectoryCopy(ctx, inspectLocalDirectory)
					: false;
				const workspace = creating
					? await session.create({
							project: args.trim() || session.project || path.basename(ctx.cwd) || "workspace",
							...(repoUrl === undefined ? {} : { repoUrl }),
						})
					: await session.connect(configuredWorkspace);
				wirePortsNotifier(ctx.ui);
				persistRemote();
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Connected to Scraps workspace ${workspace.id}.`, "info");
				if (creating) {
					if (pushDirectory) {
						await pushLocalDirectory(ctx, session, workspace.id, buildLocalArchive);
					}
					await notifyEmptyWorkspace(ctx.ui, session);
				}
			} catch (error) {
				refreshStatus(ctx.ui);
				const local = session.daemonUrl === undefined || session.daemonUrl === DEFAULT_DAEMON_URL;
				const hint =
					error instanceof ScrapdApiError
						? ""
						: local
							? " Start the local worker VM with `make up`, or attach a remote worker with `scrap attach`, then run /scrap again."
							: ` Check the worker with \`scrap status\` (${session.daemonUrl}); if it moved, re-attach with \`scrap attach\`.`;
				ctx.ui.notify(`Cannot start Scraps workspace: ${describeError(error)}.${hint}`, "error");
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
				const repoUrl = await offerRepositoryClone(ctx, detectRemote);
				const pushDirectory = repoUrl === undefined
					? await offerDirectoryCopy(ctx, inspectLocalDirectory)
					: false;
				const workspace = await session.create({
					...(project.length > 0 ? { project } : {}),
					...(repoUrl === undefined ? {} : { repoUrl }),
				});
				wirePortsNotifier(ctx.ui);
				persistRemote();
				refreshStatus(ctx.ui);
				ctx.ui.notify(`Created Scraps workspace ${workspace.id}.`, "info");
				if (pushDirectory) {
					await pushLocalDirectory(ctx, session, workspace.id, buildLocalArchive);
				}
				await notifyEmptyWorkspace(ctx.ui, session);
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

/**
 * Resolve the local checkout's origin URL for a new workspace.
 *
 * This is the only local repository inspection Scraps performs (ADR 0001:
 * minimal and visible), and the URL is always shown to the user for
 * confirmation before anything is cloned.
 */
async function detectGitRemoteUrl(cwd: string): Promise<string | undefined> {
	try {
		const { stdout } = await execFile("git", ["-C", cwd, "remote", "get-url", "origin"], {
			timeout: 5000,
		});
		const remote = stdout.trim();
		return /^(https?|ssh):\/\//.test(remote) || /^[^@/\s]+@[^:/\s]+:.+$/.test(remote) ? remote : undefined;
	} catch {
		// Not a git checkout, no origin, or git is unavailable: create empty.
		return undefined;
	}
}

/** Ask whether to clone the detected origin into the new workspace. */
async function offerRepositoryClone(
	ctx: { cwd: string; ui: { confirm: (title: string, body: string) => Promise<boolean> } },
	detectRemote: (cwd: string) => Promise<string | undefined>,
): Promise<string | undefined> {
	const remote = await detectRemote(ctx.cwd);
	if (remote === undefined) {
		return undefined;
	}
	const confirmed = await ctx.ui.confirm(
		"Clone repository into workspace?",
		`Clone ${remote} (its default branch) into the new Scraps workspace? Unpushed local changes are not included.`,
	);
	return confirmed ? remote : undefined;
}

/**
 * Offer a one-shot directory copy into the new workspace (ADR 0014). Returns
 * whether the user confirmed; the copy runs only after the workspace exists.
 * Returns false for an empty directory: there is nothing to copy.
 */
async function offerDirectoryCopy(
	ctx: { cwd: string; ui: { confirm: (title: string, body: string) => Promise<boolean> } },
	inspect: (cwd: string) => Promise<number>,
): Promise<boolean> {
	const entries = await inspect(ctx.cwd);
	if (entries === 0) {
		return false;
	}
	return ctx.ui.confirm(
		"Copy this directory into the new workspace?",
		`Copy ${entries} entr${entries === 1 ? "y" : "ies"} from ${ctx.cwd}? The copy is literal: .git and uncommitted changes are included.`,
	);
}

/** Push the local directory into the workspace, reporting the outcome. */
async function pushLocalDirectory(
	ctx: { cwd: string; ui: { notify: (message: string, level: "info" | "warning" | "error") => void } },
	session: WorkspaceSession,
	workspaceId: string,
	buildArchive: (cwd: string) => Promise<ReadableStream<Uint8Array>>,
): Promise<void> {
	try {
		const archive = await buildArchive(ctx.cwd);
		const result = await session.requireClient().pushArchive(workspaceId, archive);
		ctx.ui.notify(
			`Copied ${result.files} file${result.files === 1 ? "" : "s"} (${result.bytes} bytes) into ${workspaceId}.`,
			"info",
		);
	} catch (error) {
		ctx.ui.notify(
			`Workspace ${workspaceId} was created, but copying ${ctx.cwd} failed: ${describeError(error)}. ` +
				`Retry from the terminal with: scrap push ${workspaceId} ${ctx.cwd}`,
			"warning",
		);
	}
}

/** Count entries in a local directory; 0 when it is missing or unreadable. */
async function countLocalEntries(cwd: string): Promise<number> {
	try {
		return (await readdir(cwd)).length;
	} catch {
		return 0;
	}
}

/**
 * Stream the local directory as a tar archive, excluding Scraps' internal
 * directory. Uses the system tar so the extension carries no archive code.
 */
async function tarLocalDirectory(cwd: string): Promise<ReadableStream<Uint8Array>> {
	const child = spawn("tar", ["-cf", "-", "--exclude=.scrap", "-C", cwd, "."], {
		stdio: ["ignore", "pipe", "pipe"],
	});
	const stderr: string[] = [];
	child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk.toString()));
	const finished = new Promise<void>((resolve, reject) => {
		child.on("close", (code) => {
			if (code === 0) {
				resolve();
			} else {
				reject(new Error(stderr.join("").trim() || `tar exited with code ${code}`));
			}
		});
		child.on("error", reject);
	});
	// Surface tar failures even if nobody awaits the stream to completion.
	finished.catch(() => {});
	return Readable.toWeb(child.stdout) as ReadableStream<Uint8Array>;
}

/**
 * Tell the user when a freshly created workspace has no project files, so an
 * empty /workspace is never mistaken for a broken attachment. `.scrap` is
 * Scraps' own internal directory and does not count as project content.
 */
async function notifyEmptyWorkspace(ui: Ui, session: WorkspaceSession): Promise<void> {
	const workspace = session.connectedWorkspace;
	if (workspace === undefined) {
		return;
	}
	try {
		const entries = await session.requireClient().readdir(workspace.id, ".");
		if (entries.every((entry) => entry === ".scrap")) {
			ui.notify(
				`Workspace ${workspace.id} is empty — local files are not copied automatically. ` +
					"Run /scrap again from a git checkout to clone it, run `scrap push <workspace> <dir>` to copy a local directory, or create files from the agent side.",
				"warning",
			);
		}
	} catch {
		// Advisory only: never surface directory listing failures here.
	}
}
