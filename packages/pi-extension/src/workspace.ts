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
import {
	REQUIRED_PATH_CONTRACT,
	VIRTUAL_WORKSPACE_ROOT,
	type WorkspaceRecord,
	ScrapdClient,
} from "./client.ts";

/** Fallback project root used if the daemon record omits rootPath. */
export const DEFAULT_REMOTE_ROOT = VIRTUAL_WORKSPACE_ROOT;

/** Delay before the second port check after a command settles (ms). */
const PORT_RECHECK_DELAY_MS = 2500;

export type ConnectionState =
	| { readonly status: "disconnected"; readonly reason?: string }
	| { readonly status: "connected"; readonly workspace: WorkspaceRecord; readonly provider?: string };

export const DISCONNECTED_TOOL_MESSAGE =
	"Scraps remote workspace is not connected; local execution is disabled " +
	"(fail-closed). Reconnect with /scrap or /scrap-select <workspace>.";

export class WorkspaceSession {
	private config: RemoteConfig | undefined;
	private state: ConnectionState = { status: "disconnected" };
	private client: ScrapdClient | undefined;
	private readonly clientFactory: (baseUrl: string, token?: string) => ScrapdClient;
	private seenPorts = new Set<number>();
	private portsListener: ((ports: number[]) => void) | undefined;
	private portsRefresh: Promise<void> = Promise.resolve();
	private portsRetcheck: ReturnType<typeof setTimeout> | undefined;

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
		this.resetPorts();
	}

	/** Explicitly return project tools to the local computer. */
	deactivate(): void {
		this.config = undefined;
		this.client = undefined;
		this.state = { status: "disconnected" };
		this.resetPorts();
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

	/** Ports known to be listening inside the connected workspace. */
	get listeningPorts(): number[] {
		return [...this.seenPorts].sort((a, b) => a - b);
	}

	/** Register the callback fired when new ports start listening. */
	setPortsListener(listener: ((ports: number[]) => void) | undefined): void {
		this.portsListener = listener;
	}

	/**
	 * Refresh listening ports and report newly seen ones to the listener.
	 * Best-effort by design: failures are swallowed so a port hint can never
	 * fail a tool. Calls are serialized so concurrent checks cannot double-notify.
	 */
	refreshPorts(options: { silent?: boolean } = {}): Promise<void> {
		this.portsRefresh = this.portsRefresh.then(() => this.diffPorts(options));
		return this.portsRefresh;
	}

	/**
	 * Check ports after a command settles, then once more shortly after:
	 * backgrounded dev servers often bind just after their launcher exits.
	 */
	schedulePortsCheck(): void {
		if (!this.remoteMode) {
			return;
		}
		void this.refreshPorts();
		if (this.portsRetcheck !== undefined) {
			return;
		}
		const timer = setTimeout(() => {
			this.portsRetcheck = undefined;
			void this.refreshPorts();
		}, PORT_RECHECK_DELAY_MS);
		timer.unref?.();
		this.portsRetcheck = timer;
	}

	private async diffPorts(options: { silent?: boolean }): Promise<void> {
		if (this.state.status !== "connected") {
			return;
		}
		const id = this.state.workspace.id;
		try {
			const ports = await this.requireClient().ports(id);
			const fresh = ports.filter((port) => !this.seenPorts.has(port));
			this.seenPorts = new Set(ports);
			if (fresh.length > 0 && options.silent !== true && this.portsListener !== undefined) {
				this.portsListener(fresh);
			}
		} catch {
			// Port discovery is advisory; never surface as an error.
		}
	}

	private resetPorts(): void {
		this.seenPorts = new Set<number>();
		if (this.portsRetcheck !== undefined) {
			clearTimeout(this.portsRetcheck);
			this.portsRetcheck = undefined;
		}
	}

	get connectedWorkspace(): WorkspaceRecord | undefined {
		return this.state.status === "connected" ? this.state.workspace : undefined;
	}

	/** Remote project root used to resolve relative paths in tools. */
	get root(): string {
		return DEFAULT_REMOTE_ROOT;
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
			assertCompatibleWorkspace(workspace);
			const provider = await providerName(client);
			this.state = { status: "connected", workspace, ...(provider === undefined ? {} : { provider }) };
			this.resetPorts();
			// Seed the baseline silently: hints fire for ports that start
			// listening after attachment, not for ones already running.
			void this.refreshPorts({ silent: true });
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
	async create(options: { project?: string; repoUrl?: string } = {}): Promise<WorkspaceRecord> {
		const client = this.requireClient();
		const workspace = await client.createWorkspace({
			...(options.project === undefined ? {} : { project: options.project }),
			...(options.repoUrl === undefined ? {} : { repoUrl: options.repoUrl }),
		});
		assertCompatibleWorkspace(workspace);
		const provider = await providerName(client);
		this.state = { status: "connected", workspace, ...(provider === undefined ? {} : { provider }) };
		this.resetPorts();
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

async function providerName(client: ScrapdClient): Promise<string | undefined> {
	try {
		return (await client.info()).provider;
	} catch {
		// Workspace attachment is authoritative; provider display is best-effort.
		return undefined;
	}
}

function assertCompatibleWorkspace(workspace: WorkspaceRecord): void {
	if (
		workspace.pathContract !== REQUIRED_PATH_CONTRACT ||
		workspace.rootPath !== VIRTUAL_WORKSPACE_ROOT
	) {
		throw new ScrapsUnavailableError(
			`Incompatible scrapd path contract: expected ${REQUIRED_PATH_CONTRACT} at ` +
				`${VIRTUAL_WORKSPACE_ROOT}, got ${workspace.pathContract ?? "missing"} at ` +
				`${workspace.rootPath ?? "missing"}. Upgrade scrapd and the Scraps Pi extension together.`,
		);
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
		const ports = session.listeningPorts;
		const listening = ports.length > 0 ? ` · ${formatPorts(ports)}` : "";
		return `scrap:${connection.provider ?? "remote"}:${connection.workspace.id}:${connection.workspace.state}${listening}`;
	}
	return "scrap:disconnected";
}

/** Ports as `:5173, :3000` for status lines and hints. */
export function formatPorts(ports: readonly number[]): string {
	return ports.map((port) => `:${port}`).join(", ");
}

/** Short hint shown when workspace ports start listening. */
export function portsHint(ports: readonly number[]): string {
	return `${formatPorts(ports)} listening — preview with scrap open`;
}

/** Human-readable summary for the /scrap command. */
export function describeSession(session: WorkspaceSession): string {
	if (!session.remoteMode) {
		return "Pi project tools are local. Run /scrap to attach a remote workspace.";
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
			`Provider: ${connection.provider ?? "unknown"}`,
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
