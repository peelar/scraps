import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { createServer, type Server } from "node:http";

import { ScrapdClient } from "./client.ts";
import { ScrapdApiError } from "./errors.ts";

/** Raw archive routes: the fake daemon in client.test.ts speaks JSON only. */
describe("ScrapdClient archive endpoints (ADR 0014)", () => {
	it("pushes a tar body with the tar content type and bearer token", async () => {
		let receivedType = "";
		let receivedAuth = "";
		let receivedPath = "";
		let receivedBody = Buffer.alloc(0);
		const server: Server = createServer((request, response) => {
			const chunks: Buffer[] = [];
			request.on("data", (chunk: Buffer) => chunks.push(chunk));
			request.on("end", () => {
				receivedType = request.headers["content-type"] ?? "";
				receivedAuth = request.headers.authorization ?? "";
				receivedPath = request.url ?? "";
				receivedBody = Buffer.concat(chunks);
				response.setHeader("Content-Type", "application/json");
				response.end(JSON.stringify({ files: 2, bytes: receivedBody.length }));
			});
		});
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		try {
			const address = server.address();
			assert.ok(address !== null && typeof address === "object");
			const client = new ScrapdClient(`http://127.0.0.1:${address.port}`, "secret");
			const result = await client.pushArchive("ws-1", new Uint8Array([1, 2, 3]));
			assert.deepEqual(result, { files: 2, bytes: 3 });
			assert.equal(receivedType, "application/x-tar");
			assert.equal(receivedAuth, "Bearer secret");
			assert.equal(receivedPath, "/v1/workspaces/ws-1/files/archive");
		} finally {
			server.close();
		}
	});

	it("appends the replace flag for replace pushes", async () => {
		let receivedPath = "";
		const server: Server = createServer((request, response) => {
			request.resume();
			request.on("end", () => {
				receivedPath = request.url ?? "";
				response.setHeader("Content-Type", "application/json");
				response.end(JSON.stringify({ files: 0, bytes: 0 }));
			});
		});
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		try {
			const address = server.address();
			assert.ok(address !== null && typeof address === "object");
			const client = new ScrapdClient(`http://127.0.0.1:${address.port}`);
			await client.pushArchive("ws-1", new Uint8Array([1]), true);
			assert.equal(receivedPath, "/v1/workspaces/ws-1/files/archive?replace=true");
		} finally {
			server.close();
		}
	});

	it("surfaces structured daemon errors from a rejected push", async () => {
		const server: Server = createServer((request, response) => {
			request.resume();
			request.on("end", () => {
				response.writeHead(409, { "Content-Type": "application/json" });
				response.end(JSON.stringify({ error: { code: "workspace_not_empty", message: "workspace is not empty" } }));
			});
		});
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		try {
			const address = server.address();
			assert.ok(address !== null && typeof address === "object");
			const client = new ScrapdClient(`http://127.0.0.1:${address.port}`);
			const error = await client.pushArchive("ws-1", new Uint8Array([1])).then(
				() => null,
				(thrown: unknown) => thrown,
			);
			assert.ok(error instanceof ScrapdApiError);
			assert.equal(error.status, 409);
			assert.equal(error.code, "workspace_not_empty");
		} finally {
			server.close();
		}
	});
});
