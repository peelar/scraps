import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createEditTool, createLsTool } from "@earendil-works/pi-coding-agent";

import { type WorkspaceRecord, ScrapdClient } from "./client.ts";
import { ScrapsUnavailableError } from "./errors.ts";
import {
	buildGrepCommand,
	createRemoteEditOps,
	createRemoteLsOps,
	runRemoteGrep,
	shQuote,
} from "./operations.ts";
import { type RemoteConfig } from "./identity.ts";
import { WorkspaceSession } from "./workspace.ts";

/**
 * A ScrapdClient double backed by in-memory state: enough of the fs/exec
 * surface to exercise the operations layer and Pi's own tool factories.
 */
function makeFakeClient(): {
	client: ScrapdClient;
	files: Map<string, Buffer>;
	execLog: { command: string; cwd: string }[];
	setExecOutput: (output: string, code?: number) => void;
} {
	const files = new Map<string, Buffer>();
	const execLog: { command: string; cwd: string }[] = [];
	let execOutput = "";
	let execCode = 0;

	const record: WorkspaceRecord = {
		id: "quiet-river",
		project: "owner/project",
		status: "running",
		root: "/work/project",
	};

	const client = {
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
		stat: async (_id: string, path: string) => ({
			directory: !path.includes(".") || path.endsWith("/"),
		}),
		readdir: async () => ["src", "package.json"],
		exec: async (_id: string, command: string, cwd: string, options: { onData: (c: Buffer) => void }) => {
			execLog.push({ command, cwd });
			if (execOutput.length > 0) {
				options.onData(Buffer.from(execOutput));
			}
			return { exitCode: execCode };
		},
	} as unknown as ScrapdClient;

	return {
		client,
		files,
		execLog,
		setExecOutput: (output: string, code = 0) => {
			execOutput = output;
			execCode = code;
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

async function connect(session: WorkspaceSession): Promise<void> {
	await session.connect();
}

describe("remote edit operations (exact-match preserved)", () => {
	it("applies an exact-match edit through Pi's own edit tool remotely", async () => {
		const fake = makeFakeClient();
		fake.files.set("/work/project/src/app.ts", Buffer.from("const x = 1;\n"));
		const session = makeSession(fake.client);
		await connect(session);

		const edit = createEditTool("/work/project", {
			operations: createRemoteEditOps(session),
		});

		const result = await edit.execute(
			"call-1",
			{
				path: "src/app.ts",
				edits: [{ oldText: "const x = 1;", newText: "const x = 2;" }],
			},
			undefined,
			undefined,
			undefined,
		);

		assert.equal(
			fake.files.get("/work/project/src/app.ts")?.toString("utf8"),
			"const x = 2;\n",
		);
		assert.ok(result.content.some((part) => part.type === "text"));
	});

	it("rejects a non-matching edit like the local tool", async () => {
		const fake = makeFakeClient();
		fake.files.set("/work/project/src/app.ts", Buffer.from("const x = 1;\n"));
		const session = makeSession(fake.client);
		await connect(session);

		const edit = createEditTool("/work/project", {
			operations: createRemoteEditOps(session),
		});

		await assert.rejects(
			() =>
				edit.execute(
					"call-1",
					{
						path: "src/app.ts",
						edits: [{ oldText: "not present", newText: "anything" }],
					},
					undefined,
					undefined,
					undefined,
				),
		);
		assert.equal(
			fake.files.get("/work/project/src/app.ts")?.toString("utf8"),
			"const x = 1;\n",
		);
	});
});

describe("remote read/write transport", () => {
	it("round-trips file content through the operations contract", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client);
		await connect(session);

		const ops = createRemoteEditOps(session);
		await ops.writeFile("/work/project/notes.txt", "héllo scraps\n");
		const readBack = await ops.readFile("/work/project/notes.txt");

		assert.equal(readBack.toString("utf8"), "héllo scraps\n");
	});
});

describe("remote ls operations", () => {
	it("lists entries through the client", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client);
		await connect(session);

		const ls = createLsTool("/work/project", { operations: createRemoteLsOps(session) });
		const result = await ls.execute("call-1", { path: "." }, undefined, undefined, undefined);

		const text = result.content
			.filter((part): part is { type: "text"; text: string } => part.type === "text")
			.map((part) => part.text)
			.join("");
		assert.ok(text.includes("src"));
		assert.ok(text.includes("package.json"));
	});
});

describe("remote grep", () => {
	it("builds a safely quoted rg command", () => {
		const command = buildGrepCommand({
			pattern: "don't",
			path: "src/dir with space",
			glob: "*.ts",
			ignoreCase: true,
			literal: true,
			context: 2,
		});
		assert.ok(command.startsWith("rg --line-number --no-heading --with-filename --color never"));
		assert.ok(command.includes("--ignore-case"));
		assert.ok(command.includes("--fixed-strings"));
		assert.ok(command.includes("--context 2"));
		assert.ok(command.includes(`--glob ${shQuote("*.ts")}`));
		assert.ok(command.includes(shQuote("don't")));
		assert.ok(command.endsWith(shQuote("src/dir with space")));
	});

	it("returns streamed matches from the workspace", async () => {
		const fake = makeFakeClient();
		fake.setExecOutput("src/a.ts:1:hello\nsrc/b.ts:7:hello\n");
		const session = makeSession(fake.client);
		await connect(session);

		const result = await runRemoteGrep(session, { pattern: "hello" });

		assert.equal(result.text, "src/a.ts:1:hello\nsrc/b.ts:7:hello");
		assert.equal(fake.execLog[0]?.cwd, "/work/project");
	});

	it("treats ripgrep exit code 1 as no matches", async () => {
		const fake = makeFakeClient();
		fake.setExecOutput("", 1);
		const session = makeSession(fake.client);
		await connect(session);

		const result = await runRemoteGrep(session, { pattern: "nothing" });
		assert.equal(result.text, "");
	});

	it("surfaces real rg failures", async () => {
		const fake = makeFakeClient();
		fake.setExecOutput("rg: malformed regex", 2);
		const session = makeSession(fake.client);
		await connect(session);

		await assert.rejects(() => runRemoteGrep(session, { pattern: "[" }));
	});

	it("applies the match limit", async () => {
		const fake = makeFakeClient();
		fake.setExecOutput("a:1:x\nb:2:x\nc:3:x\n");
		const session = makeSession(fake.client);
		await connect(session);

		const result = await runRemoteGrep(session, { pattern: "x", limit: 2 });
		assert.equal(result.text, "a:1:x\nb:2:x");
		assert.equal(result.matchLimitReached, 2);
	});
});

describe("fail-closed gate", () => {
	it("refuses operations while disconnected", async () => {
		const fake = makeFakeClient();
		const session = makeSession(fake.client); // never connected

		await assert.rejects(
			() => runRemoteGrep(session, { pattern: "x" }),
			ScrapsUnavailableError,
		);
		const ops = createRemoteEditOps(session);
		await assert.rejects(() => ops.readFile("/work/project/src/app.ts"), ScrapsUnavailableError);
	});
});
