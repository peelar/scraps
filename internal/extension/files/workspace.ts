/**
 * Workspace session state.
 *
 * A WorkspaceSession tracks the extension's attachment to a Scraps workspace
 * for the lifetime of a Pi session. It is the fail-closed gate used by every
 * remote tool operation: when the session is not connected, accessors throw
 * instead of letting a tool touch the local machine.
 *
 * Pi session identity and Scraps workspace identity are distinct (ADR 0001):
 * a session may reconnect to its previous workspace, select a different
 * workspace mid-session, or run without one until `/scrap-select`.
 */

import { type RemoteConfig } from "./identity.ts";
import { ScrapsUnavailableError } from "./errors.ts";
import { type WorkspaceRecord, ScrapdClient } from "./client.ts";

/** Fallback project root used if the daemon record omits rootPath. */
export const DEFAULT_REMOTE_ROOT = "/workspace";

export type ConnectionState =
	| { readonly status: "disconnected"; readonly reason?: string }
	| { readonly status: "connected"; readonly workspace: WorkspaceRecord };

export const DISCONNECTED_TOOL_MESSAGE =
	"Scraps remote workspace is not connected; local execution is disabled " +
	"(fail-closed). Reconnect with /scrap or /scrap-select <workspace>.";

export class WorkspaceSession {
	private config: RemoteConfig | undefined;
	private state: ConnectionState = { status: "disconnected" };
	private client: ScrapdClient | undefined;
	private readonly clientFactory: (baseUrl: string, token?: string) => ScrapdClient;

	constructor(
		clientFactory: (baseUrl: string, token?: string) => ScrapdClient = (baseUrl, token) =>
			new ScrapdClient(baseUrl, token),
	) {
		this.clientFactory = clientFactory;
	}

	/** Reconfigure (at session_start) and drop any previous connection. */
	configure(config: RemoteConfig): void {
		this.config = config;
		this.client =
			config.daemonUrl === undefined
				? undefined
				: this.clientFactory(config.daemonUrl, config.token);
		this.state = { status: "disconnected" };
	}

	get remoteMode(): boolean {
		return this.config !== undefined;
	}

	get daemonUrl(): string | undefined {
		return this.config?.daemonUrl;
	}

	/** Project hint from the environment, when the daemon record lacks one. */
	get project(): string | undefined {
		return this.config?.project;
	}

	/** Workspace id to attach to: the connected one, else the configured one. */
	get workspaceId(): string | undefined {
		if (this.state.status === "connected") {
			return this.state.workspace.id;
		}
		return this.config?.workspaceId;
	}

	get connection(): ConnectionState {
		return this.state;
	}

	get connectedWorkspace(): WorkspaceRecord | undefined {
		return this.state.status === "connected" ? this.state.workspace : undefined;
	}

	/** Remote project root used to resolve relative paths in tools. */
	get root(): string {
		const rootPath = this.connectedWorkspace?.rootPath;
		return rootPath !== undefined && rootPath.length > 0 ? rootPath : DEFAULT_REMOTE_ROOT;
	}

	/** Client for scrapd; throws when the extension is misconfigured. */
	requireClient(): ScrapdClient {
		if (!this.remoteMode) {
			throw new ScrapsUnavailableError("Scraps remote mode is not active.");
		}
		if (this.client === undefined) {
			throw new ScrapsUnavailableError(
				"SCRAP_DAEMON_URL is not set; the Scraps extension cannot reach a control daemon.",
			);
		}
		return this.client;
	}

	/**
	 * Fail-closed gate for tool operations: only a verified, connected
	 * workspace may execute project work.
	 */
	requireWorkspace(): WorkspaceRecord {
		if (!this.remoteMode) {
			throw new ScrapsUnavailableError("Scraps remote mode is not active.");
		}
		if (this.state.status !== "connected" || this.client === undefined) {
			throw new ScrapsUnavailableError(DISCONNECTED_TOOL_MESSAGE);
		}
		return this.state.workspace;
	}

	/** Verify a workspace through scrapd and connect to it. */
	async connect(workspaceId?: string): Promise<WorkspaceRecord> {
		const id = workspaceId ?? this.workspaceId;
		if (id === undefined) {
			throw new ScrapsUnavailableError(
				"No Scraps workspace selected. Pass --workspace, set SCRAP_WORKSPACE_ID, " +
					"or use /scrap-select <workspace>.",
			);
		}
		const client = this.requireClient();
		try {
			const workspace = await client.getWorkspace(id);
			this.state = { status: "connected", workspace };
			return workspace;
		} catch (error) {
			this.state = {
				status: "disconnected",
				reason: describeError(error),
			};
			throw error;
		}
	}

	/** Create a workspace through scrapd and connect to it. */
	async create(project?: string): Promise<WorkspaceRecord> {
		const client = this.requireClient();
		const workspace = await client.createWorkspace({
			...(project === undefined ? {} : { project }),
		});
		this.state = { status: "connected", workspace };
		return workspace;
	}

	/** Drop the connection (used when the workspace is deleted). */
	disconnect(reason?: string): void {
		this.state =
			reason === undefined
				? { status: "disconnected" }
				: { status: "disconnected", reason };
	}

	async list(): Promise<WorkspaceRecord[]> {
		return this.requireClient().listWorkspaces();
	}

	async remove(): Promise<void> {
		const workspace = this.requireWorkspace();
		await this.requireClient().deleteWorkspace(workspace.id);
		this.disconnect(`workspace ${workspace.id} deleted`);
	}
}

export function describeError(error: unknown): string {
	if (error instanceof Error) {
		return error.message;
	}
	return String(error);
}

/** Footer status text; always visible while remote mode is active (ADR 0001). */
export function statusText(session: WorkspaceSession): string | undefined {
	if (!session.remoteMode) {
		return undefined;
	}
	const connection = session.connection;
	if (connection.status === "connected") {
		return `scrap:${connection.workspace.id}`;
	}
	return "scrap:disconnected";
}

/** Human-readable summary for the /scrap command. */
export function describeSession(session: WorkspaceSession): string {
	if (!session.remoteMode) {
		return "Pi is running locally without Scraps (--scrap not given).";
	}

	const lines = [
		"Scraps remote workspace mode is active.",
		`Daemon: ${session.daemonUrl ?? "not configured (set SCRAP_DAEMON_URL)"}`,
		`Workspace: ${session.workspaceId ?? "none selected"}`,
	];

	const connection = session.connection;
	if (connection.status === "connected") {
		lines.push(
			`Status: connected (${connection.workspace.state})`,
			`Project: ${connection.workspace.project ?? session.project ?? "unknown"}`,
			`Remote root: ${session.root}`,
		);
	} else {
		lines.push("Status: disconnected — project tools fail closed until connected");
		if (connection.reason !== undefined) {
			lines.push(`Reason: ${connection.reason}`);
		}
	}

	return lines.join("\n");
}
