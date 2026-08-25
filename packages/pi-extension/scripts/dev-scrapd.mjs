/**
 * dev-scrapd: a minimal fake scrapd for developing the Pi extension before
 * the real Go daemon implements the workspace API (see src/client.ts for the
 * contract).
 *
 * Workspaces are fake; the filesystem lives under a temporary sandbox
 * directory and exec runs real local processes with cwd pinned inside it.
 *
 * Usage:
 *   node scripts/dev-scrapd.mjs            # listens on 127.0.0.1:8484
 *   SCRAPD_PORT=9000 node scripts/dev-scrapd.mjs
 *
 * Then:
 *   SCRAP_DAEMON_URL=http://127.0.0.1:8484 SCRAP_WORKSPACE_ID=dev \
 *     pi -e ./src/index.ts --scrap
 */

import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, readdir, stat, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { join } from "node:path";
import { tmpdir } from "node:os";

const port = Number(process.env.SCRAPD_PORT ?? 8484);
const root = await mkdtemp(join(tmpdir(), "scrapd-"));
await mkdir(join(root, "project"), { recursive: true });
await writeFile(join(root, "project", "notes.txt"), "hello from dev-scrapd\n");

const workspaces = new Map([
	["dev", { id: "dev", project: "local/dev", status: "running", root: join(root, "project") }],
]);

const json = (response, status, payload) => {
	response.writeHead(status, { "Content-Type": "application/json" });
	response.end(JSON.stringify(payload));
};

const server = createServer((request, response) => {
	const chunks = [];
	request.on("data", (chunk) => chunks.push(chunk));
	request.on("end", () => {
		const body = chunks.length > 0 ? JSON.parse(Buffer.concat(chunks).toString()) : {};
		const route = `${request.method} ${request.url}`;
		const workspaceId = request.url?.split("/")[3] ?? "";
		const workspace = workspaces.get(workspaceId);

		if (!workspace && route !== "GET /v1/info" && route !== "GET /v1/workspaces" && route !== "POST /v1/workspaces") {
			return json(response, 404, { error: `no such workspace: ${workspaceId}` });
		}

		if (route === "GET /v1/info") {
			return json(response, 200, { name: "dev-scrapd", version: "dev" });
		}
		if (route === "GET /v1/workspaces") {
			return json(response, 200, { workspaces: [...workspaces.values()] });
		}
		if (route === "POST /v1/workspaces") {
			const id = `ws-${workspaces.size + 1}`;
			const created = {
				id,
				project: body.project,
				status: "running",
				root: join(root, "project"),
			};
			workspaces.set(id, created);
			return json(response, 200, created);
		}
		if (route === `GET /v1/workspaces/${workspaceId}`) {
			return json(response, 200, workspace);
		}
		if (route === `POST /v1/workspaces/${workspaceId}/exec`) {
			// Absolute cwd paths are used as-is: this dev double reports its real
			// sandbox root to the extension, so tool paths are already absolute.
			const cwd = body.cwd.startsWith("/") ? body.cwd : join(workspace.root, body.cwd);
			response.writeHead(200, { "Content-Type": "application/x-ndjson" });
			const line = (event) => response.write(`${JSON.stringify(event)}\n`);
			const child = spawn("/bin/sh", ["-c", body.command], {
				cwd,
				env: { ...process.env, ...(body.env ?? {}) },
			});
			const push = (stream) => (chunk) =>
				line({ event: "data", stream, data: chunk.toString("base64") });
			child.stdout.on("data", push("stdout"));
			child.stderr.on("data", push("stderr"));
			child.on("close", (code, signal) => {
				line({ event: "exit", code, signal: signal ?? null });
				response.end();
			});
			return;
		}

		const fsRoute = route.endsWith("/fs/read")
			? "read"
			: route.endsWith("/fs/write")
				? "write"
				: route.endsWith("/fs/mkdir")
					? "mkdir"
					: route.endsWith("/fs/access")
						? "access"
						: route.endsWith("/fs/stat")
							? "stat"
							: route.endsWith("/fs/readdir")
								? "readdir"
								: undefined;
		if (fsRoute === undefined) {
			return json(response, 404, { error: `no route: ${route}` });
		}
		// Absolute paths are used as-is (see exec above); relative paths
		// resolve inside the sandbox root.
		const path = body.path.startsWith("/") ? body.path : join(root, body.path);
		const fail = (error) => json(response, 404, { error: String(error.message ?? error) });

		switch (fsRoute) {
			case "read": {
				readFile(path)
					.then((content) => json(response, 200, { content: content.toString("base64") }))
					.catch(fail);
				return;
			}
			case "write": {
				writeFile(path, Buffer.from(body.content, "base64"))
					.then(() => {
						response.writeHead(204);
						response.end();
					})
					.catch(fail);
				return;
			}
			case "mkdir": {
				mkdir(path, { recursive: true })
					.then(() => {
						response.writeHead(204);
						response.end();
					})
					.catch(fail);
				return;
			}
			case "access": {
				stat(path)
					.then(() => {
						response.writeHead(204);
						response.end();
					})
					.catch(fail);
				return;
			}
			case "stat": {
				stat(path)
					.then((entry) => json(response, 200, { directory: entry.isDirectory() }))
					.catch(fail);
				return;
			}
			case "readdir": {
				readdir(path)
					.then((entries) => json(response, 200, { entries }))
					.catch(fail);
				return;
			}
		}
	});
});

server.listen(port, "127.0.0.1", () => {
	console.log(`dev-scrapd listening on http://127.0.0.1:${port}`);
	console.log(`sandbox root: ${root}`);
	console.log(`workspaces: ${[...workspaces.keys()].join(", ")}`);
});
