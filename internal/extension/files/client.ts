/**
 * HTTP client for the scrapd workspace API.
 *
 * This module is the TypeScript statement of the initial scrapd API surface
 * required by ADR 0001 (follow-up work #2: "Define the authenticated,
 * streaming scrapd workspace API"). The Go daemon does not serve these routes
 * yet; until it does, every call fails and the extension fails closed.
 *
 * Endpoints:
 *
 *   GET    /v1/info                              daemon identity
 *   GET    /v1/workspaces                        list workspaces
 *   POST   /v1/workspaces                        create { project? }
 *   GET    /v1/workspaces/{id}                   workspace status
 *   POST   /v1/workspaces/{id}/start             start workspace
 *   POST   /v1/workspaces/{id}/stop              stop workspace
 *   DELETE /v1/workspaces/{id}                   delete workspace
 *   POST   /v1/workspaces/{id}/exec              streaming command execution
 *   POST   /v1/workspaces/{id}/fs/read           { path }
 *   POST   /v1/workspaces/{id}/fs/write          { path, content(base64) }
 *   POST   /v1/workspaces/{id}/fs/mkdir          { path }
 *   POST   /v1/workspaces/{id}/fs/access         { path, mode? }
 *   POST   /v1/workspaces/{id}/fs/stat           { path }
 *   POST   /v1/workspaces/{id}/fs/readdir        { path }
 *   GET    /v1/workspaces/{id}/previews          service previews
 *
 * `exec` responses are newline-delimited JSON events:
 *
 *   {"event":"start","pid":1234}
 *   {"event":"data","stream":"stdout","data":"<base64 chunk>"}
 *   {"event":"exit","code":0,"signal":null}
 *
 * File contents are base64-encoded so binary files round-trip safely.
 */

import { ScrapdApiError } from "./errors.ts";

export type WorkspaceStatus =
	| "creating"
	| "starting"
	| "running"
	| "stopped"
	| "deleting"
	| "unknown";

export type WorkspaceRecord = {
	readonly id: string;
	readonly project?: string;
	readonly status: WorkspaceStatus;
	/** Absolute path of the project root inside the workspace. */
	readonly root?: string;
	readonly createdAt?: string;
};

export type ServicePreview = {
	readonly name: string;
	readonly url: string;
};

export type ExecOptions = {
	/** Receives decoded output chunks as they stream in. */
	readonly onData: (chunk: Buffer, stream: "stdout" | "stderr") => void;
	/** Aborts the command when fired. */
	readonly signal?: AbortSignal;
	/** Timeout in seconds; mirrors the local bash tool contract. */
	readonly timeout?: number;
	/** Additional environment variables for the command. */
	readonly env?: NodeJS.ProcessEnv;
};

type ExecEvent =
	| { event: "start"; pid?: number }
	| { event: "data"; stream?: "stdout" | "stderr"; data: string }
	| { event: "exit"; code: number | null; signal?: string | null };

export class ScrapdClient {
	private readonly baseUrl: string;
	private readonly token: string | undefined;

	constructor(baseUrl: string, token?: string) {
		this.baseUrl = baseUrl.replace(/\/+$/, "");
		this.token = token;
	}

	async info(): Promise<{ name: string; version: string }> {
		return this.json("GET", "/v1/info");
	}

	async listWorkspaces(): Promise<WorkspaceRecord[]> {
		const body = await this.json<{ workspaces?: WorkspaceRecord[] }>(
			"GET",
			"/v1/workspaces",
		);
		return body.workspaces ?? [];
	}

	async createWorkspace(project?: string): Promise<WorkspaceRecord> {
		return this.json("POST", "/v1/workspaces", { project });
	}

	async getWorkspace(id: string): Promise<WorkspaceRecord> {
		return this.json("GET", `/v1/workspaces/${encodeURIComponent(id)}`);
	}

	async startWorkspace(id: string): Promise<WorkspaceRecord> {
		return this.json("POST", `/v1/workspaces/${encodeURIComponent(id)}/start`, {});
	}

	async stopWorkspace(id: string): Promise<WorkspaceRecord> {
		return this.json("POST", `/v1/workspaces/${encodeURIComponent(id)}/stop`, {});
	}

	async deleteWorkspace(id: string): Promise<void> {
		await this.request("DELETE", `/v1/workspaces/${encodeURIComponent(id)}`);
	}

	async listPreviews(id: string): Promise<ServicePreview[]> {
		const body = await this.json<{ previews?: ServicePreview[] }>(
			"GET",
			`/v1/workspaces/${encodeURIComponent(id)}/previews`,
		);
		return body.previews ?? [];
	}

	/**
	 * Execute a command in the workspace, streaming output through `onData`.
	 * Resolves with the process exit status once the stream ends. The stream
	 * is raced against explicit timeout/abort rejections because cancelling
	 * the response body does not reliably interrupt a stalled stream.
	 */
	async exec(
		id: string,
		command: string,
		cwd: string,
		options: ExecOptions,
	): Promise<{ exitCode: number | null }> {
		const response = await this.request(
			"POST",
			`/v1/workspaces/${encodeURIComponent(id)}/exec`,
			{
				command,
				cwd,
				timeout: options.timeout,
				...(options.env === undefined ? {} : { env: options.env }),
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
			`/v1/workspaces/${encodeURIComponent(id)}/fs/read`,
			{ path },
		);
		return Buffer.from(body.content, "base64");
	}

	async writeFile(id: string, path: string, content: string | Buffer): Promise<void> {
		await this.request(
			"POST",
			`/v1/workspaces/${encodeURIComponent(id)}/fs/write`,
			{
				path,
				content: Buffer.from(content).toString("base64"),
			},
		);
	}

	async mkdir(id: string, path: string): Promise<void> {
		await this.request(
			"POST",
			`/v1/workspaces/${encodeURIComponent(id)}/fs/mkdir`,
			{ path },
		);
	}

	/** Throws ScrapdApiError when the path is missing or not accessible. */
	async access(id: string, path: string, mode: "r" | "rw" = "r"): Promise<void> {
		await this.request(
			"POST",
			`/v1/workspaces/${encodeURIComponent(id)}/fs/access`,
			{ path, mode },
		);
	}

	async stat(id: string, path: string): Promise<{ directory: boolean }> {
		const body = await this.json<{ directory: boolean }>(
			"POST",
			`/v1/workspaces/${encodeURIComponent(id)}/fs/stat`,
			{ path },
		);
		return body;
	}

	async readdir(id: string, path: string): Promise<string[]> {
		const body = await this.json<{ entries?: string[] }>(
			"POST",
			`/v1/workspaces/${encodeURIComponent(id)}/fs/readdir`,
			{ path },
		);
		return body.entries ?? [];
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
		let exit: { exitCode: number | null } | undefined;

		const handleLine = (line: string) => {
			if (line.length === 0) {
				return;
			}
			const event = JSON.parse(line) as ExecEvent;
			if (event.event === "data") {
				options.onData(
					Buffer.from(event.data, "base64"),
					event.stream === "stderr" ? "stderr" : "stdout",
				);
			} else if (event.event === "exit") {
				exit = { exitCode: event.code };
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
		return exit;
	}
}

/** scrapd could not be reached at all. */
export class ScrapsConnectionError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "ScrapsConnectionError";
	}
}

