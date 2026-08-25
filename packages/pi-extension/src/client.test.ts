import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { createServer, type Server } from "node:http";

import { ScrapdClient, ScrapsConnectionError } from "./client.ts";
import { ScrapdApiError } from "./errors.ts";

type FakeDaemon = {
	server: Server;
	url: string;
	close: () => Promise<void>;
};

/**
 * A minimal scrapd double speaking the API surface from ADR 0001: fs routes
 * with base64 bodies, workspace lifecycle, and NDJSON streaming exec.
 */
async function startFakeDaemon(): Promise<FakeDaemon> {
	const files = new Map<string, Buffer>();

	const server = createServer((request, response) => {
		const chunks: Buffer[] = [];
		request.on("data", (chunk: Buffer) => chunks.push(chunk));
		request.on("end", () => {
			const body = chunks.length > 0 ? (JSON.parse(Buffer.concat(chunks).toString()) as never) : {};
			const respond = (status: number, payload?: unknown) => {
				if (payload === undefined) {
					response.writeHead(status);
					response.end();
					return;
				}
				response.writeHead(status, { "Content-Type": "application/json" });
				response.end(JSON.stringify(payload));
			};
			const route = `${request.method} ${request.url}`;

			if (route === "GET /v1/workspaces/ws-1") {
				respond(200, {
					id: "ws-1",
					project: "owner/project",
					status: "running",
					root: "/work/project",
				});
				return;
			}

			if (route === "POST /v1/workspaces/ws-1/fs/write") {
				const input = body as { path: string; content: string };
				files.set(input.path, Buffer.from(input.content, "base64"));
				respond(204);
				return;
			}

			if (route === "POST /v1/workspaces/ws-1/fs/read") {
				const input = body as { path: string };
				const content = files.get(input.path);
				if (content === undefined) {
					respond(404, { error: `no such file: ${input.path}` });
					return;
				}
				respond(200, { content: content.toString("base64") });
				return;
			}

			if (route === "POST /v1/workspaces/ws-1/fs/stat") {
				const input = body as { path: string };
				respond(200, { directory: input.path.endsWith("/") || !input.path.includes(".") });
				return;
			}

			if (route.startsWith("POST /v1/workspaces/") && route.endsWith("/exec")) {
				const input = body as { command: string };
				const workspaceId = request.url?.split("/")[3] ?? "";
				if (workspaceId === "ws-hang") {
					// Never sends an exit event; used for timeout/abort tests.
					response.writeHead(200, { "Content-Type": "application/x-ndjson" });
					response.write(`${JSON.stringify({ event: "start", pid: 1 })}\n`);
					// Keep the connection open without ending the stream.
					return;
				}
				response.writeHead(200, { "Content-Type": "application/x-ndjson" });
				const line = (event: unknown) => response.write(`${JSON.stringify(event)}\n`);
				line({ event: "start", pid: 4242 });
				// Exercise multi-chunk streaming: split the base64 payload so
				// chunks reassemble on the client side.
				const out = `ran: ${input.command}\n`;
				const b64 = Buffer.from(out).toString("base64");
				const mid = Math.floor(b64.length / 2);
				line({ event: "data", stream: "stdout", data: b64.slice(0, mid) });
				line({ event: "data", stream: "stdout", data: b64.slice(mid) });
				line({ event: "data", stream: "stderr", data: Buffer.from("warn\n").toString("base64") });
				response.write(JSON.stringify({ event: "exit", code: 3, signal: null }) + "\n");
				response.end();
				return;
			}

			respond(404, { error: `no route: ${route}` });
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

describe("ScrapdClient", () => {
	it("reads workspace records", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			const workspace = await client.getWorkspace("ws-1");
			assert.equal(workspace.id, "ws-1");
			assert.equal(workspace.root, "/work/project");
			assert.equal(workspace.status, "running");
		} finally {
			await daemon.close();
		}
	});

	it("round-trips binary files through base64 transport", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			const bytes = Buffer.from([0x00, 0x01, 0xfe, 0xff, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a]);
			await client.writeFile("ws-1", "/work/project/logo.bin", bytes);
			const readBack = await client.readFile("ws-1", "/work/project/logo.bin");
			assert.deepEqual(readBack, bytes);
		} finally {
			await daemon.close();
		}
	});

	it("surfaces API errors with status and message", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			try {
				await client.readFile("ws-1", "/work/project/missing.txt");
				assert.fail("expected failure");
			} catch (error) {
				assert.ok(error instanceof ScrapdApiError);
				assert.equal(error.status, 404);
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
			const result = await client.exec("ws-1", "make test", "/work/project", {
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

	it("reports timeouts for streams that never exit", async () => {
		const daemon = await startFakeDaemon();
		try {
			const client = new ScrapdClient(daemon.url);
			await assert.rejects(
				() =>
					client.exec("ws-hang", "sleep forever", "/work/project", {
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
			const pending = client.exec("ws-hang", "sleep forever", "/work/project", {
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

	it("fails closed with a connection error when scrapd is unreachable", async () => {
		const client = new ScrapdClient("http://127.0.0.1:1");
		await assert.rejects(() => client.getWorkspace("ws-1"), ScrapsConnectionError);
	});
});
