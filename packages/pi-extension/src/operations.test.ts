import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createEditTool, createLsTool } from "@earendil-works/pi-coding-agent";

import {
	type ExecOptions,
	type GrepResult,
	type WorkspaceRecord,
	type FileStat,
	ScrapdClient,
} from "./client.ts";
import { ScrapsUnavailableError } from "./errors.ts";
import { type RemoteConfig } from "./identity.ts";
import * as operations from "./operations.ts";
import {
	createRemoteBashOps,
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
	execCalls: ExecOptions[];
} {
	const files = new Map<string, Buffer>();
	const statResults = new Map<string, FileStat>();
	let grepResult: GrepResult = { matches: [], limitReached: false };
	const execCalls: ExecOptions[] = [];

	const record: WorkspaceRecord = {
		id: "quiet-river",
		project: "owner/project",
		state: "running",
		rootPath: "/workspace",
		pathContract: "workspace-relative-v1",
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
		exec: async (
			_id: string,
			_command: string,
			_cwd: string,
			options: ExecOptions,
		) => {
			execCalls.push(options);
			return { exitCode: 0 };
		},
	}) as unknown as ScrapdClient;

	return {
		client,
		files,
		statResults,
		execCalls,
		get grepResult() {
			return grepResult;
		},
		set grepResult(value: GrepResult) {
			grepResult = value;
		},
	};
}

describe("remote bash environment boundary", () => {
	it("forwards only the separately approved environment", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client);
		await session.connect();
		const ops = createRemoteBashOps(session, {
			DATABASE_URL: "postgres://sentinel-approved",
		});

		await ops.exec("env", "/workspace", {
			onData: () => {},
			env: {
				AWS_SECRET_ACCESS_KEY: "sentinel-aws",
				NPM_TOKEN: "sentinel-npm",
				OPENAI_API_KEY: "sentinel-openai",
				SCRAP_TOKEN: "sentinel-scrap",
				SSH_AUTH_SOCK: "/tmp/sentinel-agent.sock",
				UNRELATED_SECRET: "sentinel-unrelated",
			},
		});

		assert.equal(fake.execCalls.length, 1);
		assert.deepEqual(fake.execCalls[0]?.env, {
			DATABASE_URL: "postgres://sentinel-approved",
		});
		assert.equal("UNRELATED_SECRET" in (fake.execCalls[0]?.env ?? {}), false);
	});
});

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
		fake.files.set("/workspace/src/app.ts", Buffer.from("const x = 1;\n"));
		const session = makeSession(fake.client);
		await session.connect();

		const edit = createEditTool("/workspace", {
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
			fake.files.get("/workspace/src/app.ts")?.toString("utf8"),
			"const x = 2;\n",
		);
	});

	it("rejects a non-matching edit like the local tool", async () => {
		const fake = makeFakeClient();
		fake.files.set("/workspace/src/app.ts", Buffer.from("const x = 1;\n"));
		const session = makeSession(fake.client);
		await session.connect();

		const edit = createEditTool("/workspace", {
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
			fake.files.get("/workspace/src/app.ts")?.toString("utf8"),
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
		await ops.writeFile("/workspace/notes.txt", "héllo scraps\n");
		const readBack = await ops.readFile("/workspace/notes.txt");

		assert.equal(readBack.toString("utf8"), "héllo scraps\n");
	});
});

describe("remote ls operations", () => {
	it("lists entries through the client", async () => {
		const fake = makeFakeClient();
		fake.statResults.set("/workspace/src", {
			exists: true,
			isDirectory: true,
			size: 0,
			mode: "drwxr-xr-x",
			modTimeMs: 0,
		});
		fake.statResults.set("/workspace/package.json", {
			exists: true,
			isDirectory: false,
			size: 42,
			mode: "-rw-r--r--",
			modTimeMs: 0,
		});
		fake.statResults.set("/workspace", {
			exists: true,
			isDirectory: true,
			size: 0,
			mode: "drwxr-xr-x",
			modTimeMs: 0,
		});
		fake.statResults.set("/workspace/src", {
			exists: true,
			isDirectory: true,
			size: 0,
			mode: "drwxr-xr-x",
			modTimeMs: 0,
		});
		fake.statResults.set("/workspace/package.json", {
			exists: true,
			isDirectory: false,
			size: 42,
			mode: "-rw-r--r--",
			modTimeMs: 0,
		});
		const session = makeSession(fake.client);
		await session.connect();

		const ls = createLsTool("/workspace", {
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
					path: "/workspace/src/a.ts",
					lineNumber: 1,
					lineText: "hello",
					lines: [],
				},
				{
					path: "/workspace/src/b.ts",
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
					path: "/workspace/src/a.ts",
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
				{ path: "/workspace/a", lineNumber: 1, lineText: "x", lines: [] },
				{ path: "/workspace/b", lineNumber: 1, lineText: "x", lines: [] },
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
		assert.equal(relativeToRoot("/workspace/src/a.ts", "/workspace"), "src/a.ts");
		assert.equal(relativeToRoot("/elsewhere/a.ts", "/workspace"), "/elsewhere/a.ts");
	});

	it("resolves relative grep paths against the workspace root", () => {
		const { resolveRemotePath } = operations;
		assert.equal(resolveRemotePath(undefined, "/workspace"), "/workspace");
		assert.equal(resolveRemotePath(".", "/workspace"), "/workspace");
		assert.equal(resolveRemotePath("src", "/workspace"), "/workspace/src");
		assert.equal(resolveRemotePath("/abs", "/workspace"), "/abs");
	});
});

describe("fail-closed gate", () => {
	it("refuses operations while disconnected", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client); // never connected

		await assert.rejects(() => runRemoteGrep(session, { pattern: "x" }), ScrapsUnavailableError);
		const ops = createRemoteEditOps(session);
		await assert.rejects(
			() => ops.readFile("/workspace/src/app.ts"),
			ScrapsUnavailableError,
		);
	});
});
