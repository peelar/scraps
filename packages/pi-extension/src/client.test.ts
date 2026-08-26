import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { createServer, type Server } from "node:http";

import { ScrapdClient, ScrapsConnectionError, workspaceRelativePath } from "./client.ts";
import { ScrapdApiError } from "./errors.ts";

type FakeDaemon = {
	server: Server;
	url: string;
	close: () => Promise<void>;
};

/**
 * A minimal scrapd double speaking the ADR 0002 API: /files routes with
 * base64 bodies, type-keyed NDJSON exec events, and nested error objects.
 */
async function startFakeDaemon(): Promise<FakeDaemon> {
	const files = new Map<string, Buffer>();

	const server = createServer((request, response) => {
		const chunks: Buffer[] = [];
		request.on("data", (chunk: Buffer) => chunks.push(chunk));
		request.on("end", () => {
			const body = chunks.length > 0 ? (JSON.parse(Buffer.concat(chunks).toString()) as never) : {};
			const apiError = (status: number, code: string, message: string) => {
				response.writeHead(status, { "Content-Type": "application/json" });
				response.end(JSON.stringify({ error: { code, message } }));
			};
			const route = `${request.method} ${request.url}`;

			if (route === "GET /v1/workspaces/ws-1") {
				response.writeHead(200, { "Content-Type": "application/json" });
				response.end(
					JSON.stringify({
						id: "ws-1",
						project: "owner/project",
						state: "running",
						rootPath: "/workspace",
						pathContract: "workspace-relative-v1",
					}),
				);
				return;
			}

			if (route === "POST /v1/workspaces/ws-1/files/write") {
				const input = body as { path: string; content: string };
				files.set(input.path, Buffer.from(input.content, "base64"));
				response.writeHead(200, { "Content-Type": "application/json" });
				response.end(JSON.stringify({ size: files.get(input.path)?.byteLength ?? 0 }));
				return;
			}

			if (route === "POST /v1/workspaces/ws-1/files/read") {
				const input = body as { path: string };
				const content = files.get(input.path);
				if (content === undefined) {
					apiError(404, "not_found", `no such file: ${input.path}`);
					return;
				}
				response.writeHead(200, { "Content-Type": "application/json" });
				response.end(JSON.stringify({ content: content.toString("base64"), size: content.byteLength }));
				return;
			}

			if (route === "POST /v1/workspaces/ws-1/files/glob") {
				const input = body as { pattern: string };
				const hits = [...files.keys()].filter((path) => path.endsWith(input.pattern.replace(/\*/g, "")));
				response.writeHead(200, { "Content-Type": "application/json" });
				response.end(JSON.stringify({ paths: hits }));
				return;
			}

			if (route === "POST /v1/workspaces/ws-1/files/grep") {
				const input = body as { pattern: string };
				const matches = [...files.entries()]
					.filter(([, content]) => content.toString("utf8").includes(input.pattern))
					.map(([path, content]) => ({
						path,
						lineNumber: 1,
						lineText: content.toString("utf8").split("\n")[0] ?? "",
						lines: [],
					}));
				response.writeHead(200, { "Content-Type": "application/json" });
				response.end(JSON.stringify({ matches, limitReached: false }));
				return;
			}

			if (route.startsWith("POST /v1/workspaces/") && route.endsWith("/exec")) {
				const workspaceId = request.url?.split("/")[3] ?? "";
				if (workspaceId === "ws-hang") {
					// Never sends an exit event; used for timeout/abort tests.
					response.writeHead(200, { "Content-Type": "application/x-ndjson" });
					response.write(`${JSON.stringify({ type: "start", pid: 1 })}\n`);
					// Keep the connection open without ending the stream.
					return;
				}
				const input = body as { command: string };
				response.writeHead(200, { "Content-Type": "application/x-ndjson" });
				const line = (event: unknown) => response.write(`${JSON.stringify(event)}\n`);
				line({ type: "start", pid: 4242 });
				// Exercise multi-chunk streaming: split the base64 payload at a
				// 4-character boundary so each chunk decodes independently.
				const out = `ran: ${input.command}\n`;
				const b64 = Buffer.from(out).toString("base64");
				const mid = Math.max(4, Math.floor(b64.length / 8) * 4);
				line({ type: "output", stream: "stdout", data: b64.slice(0, mid) });
				line({ type: "output", stream: "stdout", data: b64.slice(mid) });
				line({ type: "output", stream: "stderr", data: Buffer.from("warn\n").toString("base64") });
				response.write(JSON.stringify({ type: "exit", code: 3, durationMs: 12 }) + "\n");
				response.end();
				return;
			}

			apiError(404, "not_found", `no route: ${route}`);
		});
	});

	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	const address = server.address();
	if (address === null || typeof address === "string") {
		throw new Error("expected tcp address");
	}
	return {
		server,
		url: `http://127.0.0.1:${address.port}`,
		close: () =>
			new Promise<void>((resolve, reject) => {
				// Destroy open sockets (hanging exec streams) so close resolves.
				server.closeAllConnections();
				server.close((error) => (error === undefined ? resolve() : reject(error)));
			}),
	};
}

describe("workspaceRelativePath", () => {
	it("maps /workspace to relative API paths and rejects other roots", () => {
		assert.equal(workspaceRelativePath("/workspace"), ".");
		assert.equal(workspaceRelativePath("/workspace/src/a.ts"), "src/a.ts");
		assert.equal(workspaceRelativePath("src/a.ts"), "src/a.ts");
		assert.throws(() => workspaceRelativePath("/etc/passwd"), /outside \/workspace/);
	});
});

describe("ScrapdClient", () => {
	it("reads workspace records", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			const workspace = await client.getWorkspace("ws-1");
			assert.equal(workspace.id, "ws-1");
			assert.equal(workspace.rootPath, "/workspace");
			assert.equal(workspace.pathContract, "workspace-relative-v1");
			assert.equal(workspace.state, "running");
		} finally {
			await daemon.close();
		}
	});

	it("round-trips binary files through base64 transport", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			const bytes = Buffer.from([0x00, 0x01, 0xfe, 0xff, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a]);
			await client.writeFile("ws-1", "/workspace/logo.bin", bytes);
			const readBack = await client.readFile("ws-1", "/workspace/logo.bin");
			assert.deepEqual(readBack, bytes);
		} finally {
			await daemon.close();
		}
	});

	it("surfaces API errors with status, code, and message", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			try {
				await client.readFile("ws-1", "/workspace/missing.txt");
				assert.fail("expected failure");
			} catch (error) {
				assert.ok(error instanceof ScrapdApiError);
				assert.equal(error.status, 404);
				assert.equal(error.code, "not_found");
				assert.ok(error.message.includes("missing.txt"));
			}
		} finally {
			await daemon.close();
		}
	});

	it("streams exec output chunks and propagates exit status", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			const chunks: { data: string; stream: string }[] = [];
			const result = await client.exec("ws-1", "make test", "/workspace", {
				onData: (chunk, stream) => chunks.push({ data: chunk.toString("utf8"), stream }),
			});

			assert.equal(result.exitCode, 3);
			const stdout = chunks
				.filter((chunk) => chunk.stream === "stdout")
				.map((chunk) => chunk.data)
				.join("");
			assert.equal(stdout, "ran: make test\n");
			assert.deepEqual(
				chunks.filter((chunk) => chunk.stream === "stderr").map((chunk) => chunk.data),
				["warn\n"],
			);
		} finally {
			await daemon.close();
		}
	});

	it("translates server-side timeout exits to the local tool's message", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			await assert.rejects(
				() =>
					client.exec("ws-hang", "sleep forever", "/workspace", {
						onData: () => {},
						timeout: 1,
					}),
				(error: unknown) => error instanceof Error && error.message === "timeout:1",
			);
		} finally {
			await daemon.close();
		}
	});

	it("reports aborts when the signal fires", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			const controller = new AbortController();
			const pending = client.exec("ws-hang", "sleep forever", "/workspace", {
				onData: () => {},
				signal: controller.signal,
			});
			setTimeout(() => controller.abort(), 20);
			await assert.rejects(
				() => pending,
				(error: unknown) => error instanceof Error && error.message === "aborted",
			);
		} finally {
			await daemon.close();
		}
	});

	it("sends the bearer token when configured", async () => {
		const daemon = await startFakeDaemon();
		try {
			// The fake daemon ignores auth; assert the header is present.
			let sawHeader = false;
			daemon.server.on("request", (request) => {
				if (request.headers.authorization === "Bearer secret") {
					sawHeader = true;
				}
			});
			const client = new ScrapdClient(daemon.url, "secret");
			await client.getWorkspace("ws-1");
			assert.ok(sawHeader);
		} finally {
			await daemon.close();
		}
	});

	it("queries glob and grep endpoints", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			await client.writeFile("ws-1", "/workspace/src/a.ts", "hello world\n");
			const paths = await client.glob("ws-1", { pattern: "*.ts", path: "/workspace" });
			assert.ok(paths.includes("src/a.ts"));
			const grep = await client.grep("ws-1", { pattern: "hello" });
			assert.equal(grep.matches.length, 1);
			assert.equal(grep.matches[0]?.path, "src/a.ts");
		} finally {
			await daemon.close();
		}
	});

	it("fails closed with a connection error when scrapd is unreachable", async () => {
		const client = new ScrapdClient("http://127.0.0.1:1");
		await assert.rejects(() => client.getWorkspace("ws-1"), ScrapsConnectionError);
	});
});
