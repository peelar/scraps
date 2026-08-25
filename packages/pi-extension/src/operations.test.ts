import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createEditTool, createLsTool } from "@earendil-works/pi-coding-agent";

import {
	type GrepResult,
	type WorkspaceRecord,
	type FileStat,
	ScrapdClient,
} from "./client.ts";
import { ScrapsUnavailableError } from "./errors.ts";
import { type RemoteConfig } from "./identity.ts";
import * as operations from "./operations.ts";
import {
	createRemoteEditOps,
	createRemoteLsOps,
	relativeToRoot,
	runRemoteGrep,
} from "./operations.ts";
import { WorkspaceSession } from "./workspace.ts";

/**
 * A ScrapdClient double backed by in-memory state: enough of the files/search
 * surface to exercise the operations layer and Pi's own tool factories.
 */
function makeFakeClient(): {
	client: ScrapdClient;
	files: Map<string, Buffer>;
	statResults: Map<string, FileStat>;
	grepResult: GrepResult;
} {
	const files = new Map<string, Buffer>();
	const statResults = new Map<string, FileStat>();
	let grepResult: GrepResult = { matches: [], limitReached: false };

	const record: WorkspaceRecord = {
		id: "quiet-river",
		project: "owner/project",
		state: "running",
		rootPath: "/srv/workspaces/quiet-river",
	};

	const client = Object.assign(Object.create(ScrapdClient.prototype), {
		getWorkspace: async () => record,
		readFile: async (_id: string, path: string) => {
			const content = files.get(path);
			if (content === undefined) {
				throw new Error(`ENOENT: ${path}`);
			}
			return content;
		},
		writeFile: async (_id: string, path: string, content: string | Buffer) => {
			files.set(path, Buffer.from(content));
		},
		mkdir: async () => {},
		access: async (_id: string, path: string) => {
			if (!files.has(path)) {
				throw new Error(`ENOENT: ${path}`);
			}
		},
		stat: async (_id: string, path: string) => {
			const entry = statResults.get(path);
			if (entry === undefined) {
				throw new Error(`ENOENT: ${path}`);
			}
			return entry;
		},
		readdir: async () => ["src", "package.json"],
		grep: async () => grepResult,
	}) as unknown as ScrapdClient;

	return {
		client,
		files,
		statResults,
		get grepResult() {
			return grepResult;
		},
		set grepResult(value: GrepResult) {
			grepResult = value;
		},
	};
}

function makeSession(client: ScrapdClient): WorkspaceSession {
	const config: RemoteConfig = {
		daemonUrl: "http://127.0.0.1:1",
		workspaceId: "quiet-river",
		project: "owner/project",
		token: undefined,
	};
	const session = new WorkspaceSession(() => client);
	session.configure(config);
	return session;
}

describe("remote edit operations (exact-match preserved)", () => {
	it("applies an exact-match edit through Pi's own edit tool remotely", async () => {
		const fake = makeFakeClient();
		fake.files.set("/srv/workspaces/quiet-river/src/app.ts", Buffer.from("const x = 1;\n"));
		const session = makeSession(fake.client);
		await session.connect();

		const edit = createEditTool("/srv/workspaces/quiet-river", {
			operations: createRemoteEditOps(session),
		});

		await edit.execute(
			"call-1",
			{
				path: "src/app.ts",
				edits: [{ oldText: "const x = 1;", newText: "const x = 2;" }],
			},
			undefined,
			undefined,
		);

		assert.equal(
			fake.files.get("/srv/workspaces/quiet-river/src/app.ts")?.toString("utf8"),
			"const x = 2;\n",
		);
	});

	it("rejects a non-matching edit like the local tool", async () => {
		const fake = makeFakeClient();
		fake.files.set("/srv/workspaces/quiet-river/src/app.ts", Buffer.from("const x = 1;\n"));
		const session = makeSession(fake.client);
		await session.connect();

		const edit = createEditTool("/srv/workspaces/quiet-river", {
			operations: createRemoteEditOps(session),
		});

		await assert.rejects(() =>
			edit.execute(
				"call-1",
				{
					path: "src/app.ts",
					edits: [{ oldText: "not present", newText: "anything" }],
				},
				undefined,
				undefined,
			),
		);
		assert.equal(
			fake.files.get("/srv/workspaces/quiet-river/src/app.ts")?.toString("utf8"),
			"const x = 1;\n",
		);
	});
});

describe("remote read/write transport", () => {
	it("round-trips file content through the operations contract", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client);
		await session.connect();

		const ops = createRemoteEditOps(session);
		await ops.writeFile("/srv/workspaces/quiet-river/notes.txt", "héllo scraps\n");
		const readBack = await ops.readFile("/srv/workspaces/quiet-river/notes.txt");

		assert.equal(readBack.toString("utf8"), "héllo scraps\n");
	});
});

describe("remote ls operations", () => {
	it("lists entries through the client", async () => {
		const fake = makeFakeClient();
		fake.statResults.set("/srv/workspaces/quiet-river/src", {
			exists: true,
			isDirectory: true,
			size: 0,
			mode: "drwxr-xr-x",
			modTimeMs: 0,
		});
		fake.statResults.set("/srv/workspaces/quiet-river/package.json", {
			exists: true,
			isDirectory: false,
			size: 42,
			mode: "-rw-r--r--",
			modTimeMs: 0,
		});
		fake.statResults.set("/srv/workspaces/quiet-river", {
			exists: true,
			isDirectory: true,
			size: 0,
			mode: "drwxr-xr-x",
			modTimeMs: 0,
		});
		fake.statResults.set("/srv/workspaces/quiet-river/src", {
			exists: true,
			isDirectory: true,
			size: 0,
			mode: "drwxr-xr-x",
			modTimeMs: 0,
		});
		fake.statResults.set("/srv/workspaces/quiet-river/package.json", {
			exists: true,
			isDirectory: false,
			size: 42,
			mode: "-rw-r--r--",
			modTimeMs: 0,
		});
		const session = makeSession(fake.client);
		await session.connect();

		const ls = createLsTool("/srv/workspaces/quiet-river", {
			operations: createRemoteLsOps(session),
		});
		const result = await ls.execute("call-1", { path: "." }, undefined, undefined);

		const text = result.content
			.filter((part): part is { type: "text"; text: string } => part.type === "text")
			.map((part) => part.text)
			.join("");
		assert.ok(text.includes("src"));
		assert.ok(text.includes("package.json"));
	});
});

describe("remote grep (daemon search endpoint)", () => {
	it("formats matches exactly like the built-in grep", async () => {
		const fake = makeFakeClient();
		fake.grepResult = {
			matches: [
				{
					path: "/srv/workspaces/quiet-river/src/a.ts",
					lineNumber: 1,
					lineText: "hello",
					lines: [],
				},
				{
					path: "/srv/workspaces/quiet-river/src/b.ts",
					lineNumber: 7,
					lineText: "hello",
					lines: [],
				},
			],
			limitReached: false,
		};
		const session = makeSession(fake.client);
		await session.connect();

		const result = await runRemoteGrep(session, { pattern: "hello" });

		assert.equal(result.text, "src/a.ts:1: hello\nsrc/b.ts:7: hello");
		assert.equal(result.details, undefined);
	});

	it("formats context lines with the built-in separators", async () => {
		const fake = makeFakeClient();
		fake.grepResult = {
			matches: [
				{
					path: "/srv/workspaces/quiet-river/src/a.ts",
					lineNumber: 3,
					lineText: "match",
					lines: [
						{ n: 2, text: "before", match: false },
						{ n: 3, text: "match", match: true },
					],
				},
			],
			limitReached: false,
		};
		const session = makeSession(fake.client);
		await session.connect();

		const result = await runRemoteGrep(session, { pattern: "match", context: 1 });

		assert.equal(result.text, "src/a.ts-2- before\nsrc/a.ts:3: match");
	});

	it("reports no matches like the built-in tool", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client);
		await session.connect();

		const result = await runRemoteGrep(session, { pattern: "nothing" });
		assert.equal(result.text, "No matches found");
		assert.equal(result.details, undefined);
	});

	it("surfaces the match-limit notice with details", async () => {
		const fake = makeFakeClient();
		fake.grepResult = {
			matches: [
				{ path: "/srv/workspaces/quiet-river/a", lineNumber: 1, lineText: "x", lines: [] },
				{ path: "/srv/workspaces/quiet-river/b", lineNumber: 1, lineText: "x", lines: [] },
			],
			limitReached: true,
		};
		const session = makeSession(fake.client);
		await session.connect();

		const result = await runRemoteGrep(session, { pattern: "x", limit: 2 });

		assert.ok(result.text.includes("a:1: x"));
		assert.ok(result.text.includes("2 matches limit reached"));
		assert.equal(result.details?.matchLimitReached, 2);
	});
});

describe("path rendering", () => {
	it("renders workspace paths relative to the root", () => {
		assert.equal(relativeToRoot("/srv/workspaces/ws-1/src/a.ts", "/srv/workspaces/ws-1"), "src/a.ts");
		assert.equal(relativeToRoot("/elsewhere/a.ts", "/srv/workspaces/ws-1"), "/elsewhere/a.ts");
	});

	it("resolves relative grep paths against the workspace root", () => {
		const { resolveRemotePath } = operations;
		assert.equal(resolveRemotePath(undefined, "/srv/workspaces/ws-1"), "/srv/workspaces/ws-1");
		assert.equal(resolveRemotePath(".", "/srv/workspaces/ws-1"), "/srv/workspaces/ws-1");
		assert.equal(resolveRemotePath("src", "/srv/workspaces/ws-1"), "/srv/workspaces/ws-1/src");
		assert.equal(resolveRemotePath("/abs", "/srv/workspaces/ws-1"), "/abs");
	});
});

describe("fail-closed gate", () => {
	it("refuses operations while disconnected", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client); // never connected

		await assert.rejects(() => runRemoteGrep(session, { pattern: "x" }), ScrapsUnavailableError);
		const ops = createRemoteEditOps(session);
		await assert.rejects(
			() => ops.readFile("/srv/workspaces/quiet-river/src/app.ts"),
			ScrapsUnavailableError,
		);
	});
});
