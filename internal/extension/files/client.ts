/**
 * HTTP client for the scrapd workspace API (ADR 0002).
 *
 * The daemon is the authority for workspace lifecycle, command execution,
 * files, and search. Conventions:
 *
 * - Base URL default `http://127.0.0.1:8484`; operations under `/v1`.
 * - If a token is configured, every request carries
 *   `Authorization: Bearer <token>`.
 * - Errors are `{ "error": { "code": "...", "message": "..." } }`.
 * - File and cwd request paths are workspace-relative. The stable
 *   agent-visible root is `/workspace`; provider host paths never cross API.
 *
 * `exec` responses are newline-delimited JSON events:
 *
 *   {"type":"start","pid":1234}
 *   {"type":"output","stream":"stdout","data":"<base64>"}
 *   {"type":"exit","code":0,"durationMs":12}
 *   {"type":"exit","code":null,"reason":"timeout","durationMs":30000}
 *
 * Closing the request stream is the abort mechanism. The daemon asks its
 * trusted provider to terminate the complete remote execution boundary.
 */

import { ScrapdApiError } from "./errors.ts";

export type WorkspaceRecord = {
	readonly id: string;
	readonly project?: string;
	readonly repoUrl?: string;
	/** OpenShell sandbox lifecycle state. */
	readonly state: string;
	/** Stable agent-visible root; currently always `/workspace`. */
	readonly rootPath?: string;
	/** Versioned path contract required by this extension. */
	readonly pathContract?: string;
	readonly createdAt?: string;
	readonly updatedAt?: string;
};

export type CreateWorkspaceInput = {
	readonly project?: string;
	readonly repoUrl?: string;
};

export type FileStat = {
	readonly exists: boolean;
	readonly isDirectory: boolean;
	readonly size: number;
	readonly mode: string;
	readonly modTimeMs: number;
};

export type GlobInput = {
	readonly pattern: string;
	readonly path?: string;
	readonly limit?: number;
};

type GrepLine = {
	readonly n: number;
	readonly text: string;
	readonly match: boolean;
};

export type GrepMatch = {
	readonly path: string;
	readonly lineNumber: number;
	readonly lineText: string;
	/** Expanded context lines; empty when context is 0 or omitted. */
	readonly lines: GrepLine[];
};

export type GrepInput = {
	readonly pattern: string;
	readonly path?: string;
	readonly glob?: string;
	readonly ignoreCase?: boolean;
	readonly literal?: boolean;
	readonly context?: number;
	readonly limit?: number;
};

export type GrepResult = {
	readonly matches: GrepMatch[];
	readonly limitReached: boolean;
};

export type RunRecord = {
	readonly id: string;
	readonly workspaceId: string;
	readonly sessionKey: string;
	readonly state: "queued" | "running" | "succeeded" | "failed" | "cancelled";
	readonly error?: string;
	readonly createdAt: string;
	readonly startedAt?: string;
	readonly finishedAt?: string;
	readonly updatedAt: string;
};

export type RunEvent = {
	readonly sequence: number;
	readonly data: Record<string, unknown>;
	readonly createdAt: string;
};

export type ExecOptions = {
	/** Receives decoded output chunks as they stream in. */
	readonly onData: (chunk: Buffer, stream: "stdout" | "stderr") => void;
	/** Aborts the command through the daemon's trusted provider boundary. */
	readonly signal?: AbortSignal;
	/** Timeout in seconds; mirrors the local bash tool contract. */
	readonly timeout?: number;
	/** Explicitly user-approved environment values; never the general Pi environment. */
	readonly env?: Readonly<Record<string, string>>;
};

type ExecEvent =
	| { type: "start"; pid?: number }
	| { type: "output"; stream?: "stdout" | "stderr"; data: string }
	| { type: "exit"; code: number | null; reason?: string; durationMs?: number };

type ExitEvent = Extract<ExecEvent, { type: "exit" }>;

export const DEFAULT_DAEMON_URL = "http://127.0.0.1:8484";
export const VIRTUAL_WORKSPACE_ROOT = "/workspace";
export const REQUIRED_PATH_CONTRACT = "workspace-relative-v1";

/** Translate stable agent-visible paths to the provider-neutral API space. */
export function workspaceRelativePath(path: string): string {
	if (path === "" || path === "." || path === VIRTUAL_WORKSPACE_ROOT || path === `${VIRTUAL_WORKSPACE_ROOT}/`) {
		return ".";
	}
	const prefix = `${VIRTUAL_WORKSPACE_ROOT}/`;
	if (path.startsWith(prefix)) return path.slice(prefix.length);
	if (path.startsWith("/")) throw new Error(`path is outside ${VIRTUAL_WORKSPACE_ROOT}: ${path}`);
	return path;
}

export class ScrapdClient {
	private readonly baseUrl: string;
	private readonly token: string | undefined;

	constructor(baseUrl: string, token?: string) {
		this.baseUrl = baseUrl.replace(/\/+$/, "");
		this.token = token;
	}

	async info(): Promise<{ name: string; version: string; provider?: string; features?: { durableRuns?: boolean; modelAuth?: boolean } }> {
		return this.json("GET", "/v1/info");
	}

	/** TCP ports listening inside the workspace, for preview hints. */
	async ports(id: string): Promise<number[]> {
		const body = await this.json<{ ports?: { port: number }[] }>(
			"GET",
			`/v1/workspaces/${encodeURIComponent(id)}/ports`,
		);
		return (body.ports ?? []).map((entry) => entry.port).sort((a, b) => a - b);
	}

	async listWorkspaces(): Promise<WorkspaceRecord[]> {
		const body = await this.json<{ workspaces?: WorkspaceRecord[] }>(
			"GET",
			"/v1/workspaces",
		);
		return body.workspaces ?? [];
	}

	async createWorkspace(input: CreateWorkspaceInput = {}): Promise<WorkspaceRecord> {
		return this.json("POST", "/v1/workspaces", {
			...(input.project === undefined ? {} : { project: input.project }),
			...(input.repoUrl === undefined ? {} : { repoUrl: input.repoUrl }),
		});
	}

	async getWorkspace(id: string): Promise<WorkspaceRecord> {
		return this.json("GET", `/v1/workspaces/${encodeURIComponent(id)}`);
	}

	async deleteWorkspace(id: string): Promise<void> {
		await this.request("DELETE", `/v1/workspaces/${encodeURIComponent(id)}`);
	}

	async startWorkspace(id: string): Promise<void> {
		await this.request("POST", `/v1/workspaces/${encodeURIComponent(id)}/start`);
	}

	async stopWorkspace(id: string): Promise<void> {
		await this.request("POST", `/v1/workspaces/${encodeURIComponent(id)}/stop`);
	}

	async createRun(id: string, prompt: string, sessionKey: string, sessionSnapshot: readonly unknown[] = []): Promise<RunRecord> {
		return this.json("POST", `/v1/workspaces/${encodeURIComponent(id)}/runs`, { prompt, sessionKey, sessionSnapshot });
	}

	async getRun(id: string): Promise<RunRecord> {
		return this.json("GET", `/v1/runs/${encodeURIComponent(id)}`);
	}

	async runEvents(id: string, after = 0): Promise<RunEvent[]> {
		const body = await this.json<{ events?: RunEvent[] }>(
			"GET",
			`/v1/runs/${encodeURIComponent(id)}/events?after=${encodeURIComponent(String(after))}`,
		);
		return body.events ?? [];
	}

	async cancelRun(id: string): Promise<void> {
		await this.request("POST", `/v1/runs/${encodeURIComponent(id)}/cancel`);
	}

	/**
	 * Execute a command in the workspace, streaming output through `onData`.
	 * Resolves with the process exit status once the stream ends.
	 *
	 * The stream is raced against explicit timeout/abort rejections because
	 * cancelling the response body does not reliably interrupt a stalled
	 * stream; closing it also cancels the daemon request context.
	 */
	async exec(
		id: string,
		command: string,
		cwd: string,
		options: ExecOptions,
	): Promise<{ exitCode: number | null }> {
		if (options.env !== undefined && Object.keys(options.env).length > 0) {
			assertSecureEnvironmentTransport(this.baseUrl);
		}
		const response = await this.request(
			"POST",
			`/v1/workspaces/${encodeURIComponent(id)}/exec`,
			{
				command,
				cwd: workspaceRelativePath(cwd),
				...(options.env === undefined || Object.keys(options.env).length === 0 ? {} : { env: options.env }),
				...(options.timeout === undefined ? {} : { timeoutMs: options.timeout * 1000 }),
			},
			options.signal,
		);
		if (response.body === null) {
			throw new ScrapdApiError(response.status, "scrapd exec returned no body");
		}
		const body = response.body;

		let fail: ((error: Error) => void) | undefined;
		const failure = new Promise<never>((_, reject) => {
			fail = reject;
		});

		const timer =
			options.timeout === undefined
				? undefined
				: setTimeout(() => {
						fail?.(new Error(`timeout:${options.timeout}`));
					}, options.timeout * 1000);

		const onAbort = () => {
			fail?.(new Error("aborted"));
		};
		if (options.signal !== undefined) {
			if (options.signal.aborted) {
				throw new Error("aborted");
			}
			options.signal.addEventListener("abort", onAbort, { once: true });
		}

		const consuming = this.consumeExecStream(body, options);
		// Keep a rejection from surfacing as unhandled if the race settles first.
		consuming.catch(() => {});

		try {
			const exit = await Promise.race([consuming, failure]);
			if (options.signal?.aborted) {
				throw new Error("aborted");
			}
			return exit;
		} catch (error) {
			if (options.signal?.aborted) {
				throw new Error("aborted");
			}
			throw error;
		} finally {
			if (timer !== undefined) {
				clearTimeout(timer);
			}
			options.signal?.removeEventListener("abort", onAbort);
			body.cancel().catch(() => {});
		}
	}

	async readFile(id: string, path: string): Promise<Buffer> {
		const body = await this.json<{ content: string }>(
			"POST",
			this.filesRoute(id, "read"),
			{ path: workspaceRelativePath(path) },
		);
		return Buffer.from(body.content, "base64");
	}

	async writeFile(id: string, path: string, content: string | Buffer): Promise<void> {
		await this.request("POST", this.filesRoute(id, "write"), {
			path: workspaceRelativePath(path),
			content: Buffer.from(content).toString("base64"),
		});
	}

	async mkdir(id: string, path: string): Promise<void> {
		await this.request("POST", this.filesRoute(id, "mkdir"), { path: workspaceRelativePath(path) });
	}

	/** Throws ScrapdApiError when the path is missing or not accessible. */
	async access(id: string, path: string, want: "read" | "write" = "read"): Promise<void> {
		await this.request("POST", this.filesRoute(id, "access"), { path: workspaceRelativePath(path), want });
	}

	async stat(id: string, path: string): Promise<FileStat> {
		return this.json("POST", this.filesRoute(id, "stat"), { path: workspaceRelativePath(path) });
	}

	async readdir(id: string, path: string): Promise<string[]> {
		const body = await this.json<{ entries?: string[] }>(
			"POST",
			this.filesRoute(id, "readdir"),
			{ path: workspaceRelativePath(path) },
		);
		return body.entries ?? [];
	}

	/** Result of an archive import (ADR 0014 directory push). */
	async pushArchive(
		id: string,
		archive: ReadableStream<Uint8Array> | Uint8Array,
		replace = false,
	): Promise<{ files: number; bytes: number }> {
		let response: Response;
		try {
			response = await fetch(
				`${this.baseUrl}/v1/workspaces/${encodeURIComponent(id)}/files/archive${replace ? "?replace=true" : ""}`,
				{
					method: "POST",
					headers: {
						"Content-Type": "application/x-tar",
						...(this.token === undefined ? {} : { Authorization: `Bearer ${this.token}` }),
					},
					body: archive,
					// undici requires half-duplex for streaming request bodies.
					...(archive instanceof Uint8Array ? {} : { duplex: "half" }),
				} as RequestInit,
			);
		} catch (error) {
			const message = error instanceof Error ? error.message : String(error);
			throw new ScrapsConnectionError(`cannot reach scrapd at ${this.baseUrl}: ${message}`);
		}
		if (!response.ok) {
			throw await ScrapdApiError.from(response);
		}
		return (await response.json()) as { files: number; bytes: number };
	}

	async glob(id: string, input: GlobInput): Promise<string[]> {
		const body = await this.json<{ paths?: string[] }>("POST", this.filesRoute(id, "glob"), {
			pattern: input.pattern,
			...(input.path === undefined ? {} : { path: workspaceRelativePath(input.path) }),
			...(input.limit === undefined ? {} : { limit: input.limit }),
		});
		return body.paths ?? [];
	}

	async grep(id: string, input: GrepInput): Promise<GrepResult> {
		return this.json("POST", this.filesRoute(id, "grep"), {
			pattern: input.pattern,
			...(input.path === undefined ? {} : { path: workspaceRelativePath(input.path) }),
			...(input.glob === undefined ? {} : { glob: input.glob }),
			...(input.ignoreCase === undefined ? {} : { ignoreCase: input.ignoreCase }),
			...(input.literal === undefined ? {} : { literal: input.literal }),
			...(input.context === undefined ? {} : { context: input.context }),
			...(input.limit === undefined ? {} : { limit: input.limit }),
		});
	}

	private filesRoute(id: string, operation: string): string {
		return `/v1/workspaces/${encodeURIComponent(id)}/files/${operation}`;
	}

	private async request(
		method: string,
		path: string,
		body?: unknown,
		signal?: AbortSignal,
	): Promise<Response> {
		let response: Response;
		try {
			response = await fetch(`${this.baseUrl}${path}`, {
				method,
				headers: {
					...(body === undefined ? {} : { "Content-Type": "application/json" }),
					...(this.token === undefined ? {} : { Authorization: `Bearer ${this.token}` }),
				},
				...(body === undefined ? {} : { body: JSON.stringify(body) }),
				...(signal === undefined ? {} : { signal }),
			});
		} catch (error) {
			if (signal?.aborted) {
				throw new Error("aborted");
			}
			const message = error instanceof Error ? error.message : String(error);
			throw new ScrapsConnectionError(`cannot reach scrapd at ${this.baseUrl}: ${message}`);
		}

		if (!response.ok) {
			throw await ScrapdApiError.from(response);
		}
		return response;
	}

	private async json<T>(method: string, path: string, body?: unknown): Promise<T> {
		const response = await this.request(method, path, body);
		return (await response.json()) as T;
	}

	private async consumeExecStream(
		body: ReadableStream<Uint8Array>,
		options: ExecOptions,
	): Promise<{ exitCode: number | null }> {
		const decoder = new TextDecoder();
		let buffer = "";
		let exit: ExitEvent | undefined;

		const handleLine = (line: string) => {
			if (line.length === 0) {
				return;
			}
			const event = JSON.parse(line) as ExecEvent;
			if (event.type === "output") {
				options.onData(
					Buffer.from(event.data, "base64"),
					event.stream === "stderr" ? "stderr" : "stdout",
				);
			} else if (event.type === "exit") {
				exit = event;
			}
		};

		for await (const chunk of body) {
			buffer += decoder.decode(chunk, { stream: true });
			let newline = buffer.indexOf("\n");
			while (newline !== -1) {
				handleLine(buffer.slice(0, newline));
				buffer = buffer.slice(newline + 1);
				newline = buffer.indexOf("\n");
			}
		}
		buffer += decoder.decode();
		handleLine(buffer);

		if (exit === undefined) {
			throw new ScrapsConnectionError("scrapd exec stream ended without an exit event");
		}
		if (exit.code === null && exit.reason !== undefined) {
			// The daemon killed the process (server-side timeout or
			// cancellation); surface it with the local tool's message shape.
			if (exit.reason === "timeout") {
				const seconds =
					options.timeout ?? Math.round((exit.durationMs ?? 0) / 1000);
				throw new Error(`timeout:${seconds}`);
			}
			throw new Error(`remote process exited: ${exit.reason}`);
		}
		return { exitCode: exit.code };
	}
}

function assertSecureEnvironmentTransport(baseUrl: string): void {
	const target = new URL(baseUrl);
	if (target.protocol === "https:") return;
	if (
		target.protocol === "http:" &&
		(target.hostname === "localhost" || target.hostname === "127.0.0.1" || target.hostname === "[::1]")
	) {
		return;
	}
	throw new Error(
		`refusing to send approved environment variables over insecure transport to ${target.origin}; use HTTPS`,
	);
}

/** scrapd could not be reached at all. */
export class ScrapsConnectionError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "ScrapsConnectionError";
	}
}
