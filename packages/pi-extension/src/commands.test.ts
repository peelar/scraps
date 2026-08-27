import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, it } from "node:test";

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { type WorkspaceRecord, ScrapdClient } from "./client.ts";
import { registerScrapCommands } from "./commands.ts";
import { ScrapdApiError } from "./errors.ts";
import type { RemoteConfig, SessionBinding } from "./identity.ts";
import { WorkspaceSession, statusText } from "./workspace.ts";

type CommandHandler = (args: string, ctx: any) => Promise<void>;

// Keep tests hermetic: defaultRemoteConfig() reads clientEnvironment() with
// the real process env, which would otherwise pick up this machine's
// ~/.config/scraps/client.json (e.g. written by `scrap attach`).
let priorProfile: string | undefined;
beforeEach(() => {
	priorProfile = process.env.SCRAPS_CLIENT_CONFIG;
	process.env.SCRAPS_CLIENT_CONFIG = "/nonexistent/scraps-test-profile.json";
});
afterEach(() => {
	if (priorProfile === undefined) {
		delete process.env.SCRAPS_CLIENT_CONFIG;
	} else {
		process.env.SCRAPS_CLIENT_CONFIG = priorProfile;
	}
});

function commandHarness(
	session: WorkspaceSession,
	options: {
		detectRemote?: (cwd: string) => Promise<string | undefined>;
		confirmAnswer?: boolean;
		inspectLocalDirectory?: (cwd: string) => Promise<number>;
		buildLocalArchive?: (cwd: string) => Promise<ReadableStream<Uint8Array>>;
	} = {},
) {
	const commands = new Map<string, CommandHandler>();
	const statuses: Array<string | undefined> = [];
	const notices: Array<{ message: string; level: string }> = [];
	const bindings: SessionBinding[] = [];
	const confirms: Array<{ title: string; body: string }> = [];
	const pi = {
		registerCommand(name: string, options: { handler: CommandHandler }) {
			commands.set(name, options.handler);
		},
	} as unknown as ExtensionAPI;
	const activate = (config: RemoteConfig) => session.configure(config);
	registerScrapCommands(
		pi,
		session,
		() => statuses.push(statusText(session)),
		activate,
		(binding) => bindings.push(binding),
		() => {},
		options.detectRemote ?? (async () => undefined),
		options.inspectLocalDirectory ?? (async () => 0),
		options.buildLocalArchive ??
			(async () => {
				throw new Error("unexpected archive build");
			}),
	);
	const ctx = {
		cwd: "/tmp/project",
		ui: {
			notify: (message: string, level: string) => notices.push({ message, level }),
			setStatus: () => {},
			confirm: async (title: string, body: string) => {
				confirms.push({ title, body });
				return options.confirmAnswer ?? true;
			},
		},
	};
	return { commands, statuses, notices, bindings, confirms, ctx };
}

function fakeClient(methods: Partial<ScrapdClient>): ScrapdClient {
	return Object.assign(Object.create(ScrapdClient.prototype), {
		info: async () => ({ name: "scrapd", version: "test", provider: "docker" }),
		...methods,
	}) as ScrapdClient;
}

const record: WorkspaceRecord = {
	id: "quiet-river",
	project: "owner/project",
	state: "running",
	rootPath: "/workspace",
	pathContract: "workspace-relative-v1",
};

describe("/scrap activation", () => {
	it("fails closed and loudly when activation cannot create a workspace", async () => {
		const client = fakeClient({
			createWorkspace: async () => { throw new Error("daemon unavailable"); },
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session);

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.equal(session.remoteMode, true);
		assert.equal(session.connectedWorkspace, undefined);
		assert.equal(harness.statuses.at(-1), "scrap:disconnected");
		assert.deepEqual(harness.bindings, [{
			version: 1,
			mode: "remote",
			daemonUrl: "http://127.0.0.1:8484",
		}]);
		assert.ok(harness.notices.at(-1)?.message.includes("daemon unavailable"));
		assert.equal(harness.notices.at(-1)?.level, "error");
	});

	it("shows actionable API errors without a connection hint", async () => {
		const client = fakeClient({
			createWorkspace: async () => {
				throw new ScrapdApiError(409, "repository access to github.com is not configured; run `scrap auth github`", "repository_auth_required");
			},
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session);

		await harness.commands.get("scrap")?.("", harness.ctx);

		const message = harness.notices.at(-1)?.message ?? "";
		assert.ok(message.includes("scrap auth github"));
		assert.ok(!message.includes("Check the worker"));
	});

	it("selects from local mode and persists the session association", async () => {
		const client = fakeClient({ getWorkspace: async () => record });
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session);

		await harness.commands.get("scrap-select")?.("quiet-river", harness.ctx);

		assert.equal(session.connectedWorkspace?.id, "quiet-river");
		assert.deepEqual(harness.bindings.at(-1), {
			version: 1,
			mode: "remote",
			daemonUrl: "http://127.0.0.1:8484",
			workspaceId: "quiet-river",
			project: "owner/project",
		});
	});
});

describe("/scrap workspace seeding", () => {
	// The extension passes the detected origin unchanged; scrapd owns URL
	// normalization so every client gets identical behavior.
	const remoteUrl = "git@github.com:peelar/scraps.git";

	it("offers the local origin remote and clones it into a new workspace", async () => {
		const created: unknown[] = [];
		const client = fakeClient({
			createWorkspace: async (input: unknown) => {
				created.push(input);
				return record;
			},
			readdir: async () => ["README.md"],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session, { detectRemote: async () => remoteUrl });

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.equal(session.connectedWorkspace?.id, "quiet-river");
		assert.deepEqual(created, [{ project: "project", repoUrl: remoteUrl }]);
		assert.equal(harness.confirms.length, 1);
		assert.ok(harness.confirms[0]?.body.includes(remoteUrl));
		// A populated workspace does not trigger the empty notice.
		assert.ok(!harness.notices.some((notice) => notice.message.includes("is empty")));
	});

	it("respects declining the clone and warns when the workspace is empty", async () => {
		const created: unknown[] = [];
		const client = fakeClient({
			createWorkspace: async (input: unknown) => {
				created.push(input);
				return record;
			},
			readdir: async () => [".scrap"],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session, {
			detectRemote: async () => remoteUrl,
			confirmAnswer: false,
		});

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.deepEqual(created, [{ project: "project" }]);
		assert.equal(harness.confirms.length, 1);
		const empty = harness.notices.find((notice) => notice.message.includes("is empty"));
		assert.ok(empty !== undefined, `expected an empty-workspace notice, got ${JSON.stringify(harness.notices)}`);
		assert.equal(empty?.level, "warning");
	});

	it("does not prompt outside a git checkout", async () => {
		const created: unknown[] = [];
		const client = fakeClient({
			createWorkspace: async (input: unknown) => {
				created.push(input);
				return record;
			},
			readdir: async () => [".scrap"],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session); // default detectRemote finds nothing

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.deepEqual(created, [{ project: "project" }]);
		assert.equal(harness.confirms.length, 0);
	});
});

describe("/scrap toss", () => {
	it("deletes the workspace before returning Pi to local mode", async () => {
		let deleted = "";
		const client = fakeClient({
			getWorkspace: async () => record,
			deleteWorkspace: async (id: string) => { deleted = id; },
		});
		const session = new WorkspaceSession(() => client);
		session.configure({
			daemonUrl: "http://worker:8484",
			workspaceId: "quiet-river",
			project: "owner/project",
			token: undefined,
		});
		await session.connect();
		const harness = commandHarness(session);

		await harness.commands.get("scrap")?.("toss", harness.ctx);

		assert.equal(deleted, "quiet-river");
		assert.equal(session.remoteMode, false);
		assert.deepEqual(harness.bindings.at(-1), { version: 1, mode: "local" });
		assert.equal(harness.statuses.at(-1), undefined);
		assert.ok(harness.notices.at(-1)?.message.includes("Tossed quiet-river"));
	});

	it("stays remote and fail-closed when deletion fails", async () => {
		const client = fakeClient({
			getWorkspace: async () => record,
			deleteWorkspace: async () => { throw new Error("gateway unavailable"); },
		});
		const session = new WorkspaceSession(() => client);
		session.configure({
			daemonUrl: "http://worker:8484",
			workspaceId: "quiet-river",
			project: "owner/project",
			token: undefined,
		});
		await session.connect();
		const harness = commandHarness(session);

		await harness.commands.get("scrap")?.("toss", harness.ctx);

		assert.equal(session.remoteMode, true);
		assert.equal(session.connectedWorkspace?.id, "quiet-river");
		assert.equal(harness.bindings.length, 0);
		assert.ok(harness.notices.at(-1)?.message.includes("Still using the remote workspace"));
	});
});

describe("/scrap directory copy (ADR 0014)", () => {
	function archiveStream(): Promise<ReadableStream<Uint8Array>> {
		const stream = new ReadableStream<Uint8Array>({
			start(controller) {
				controller.enqueue(new Uint8Array([0x01, 0x02]));
				controller.close();
			},
		});
		return Promise.resolve(stream);
	}

	it("offers a literal copy when there is no git remote and the directory has files", async () => {
		const pushes: Array<{ id: string; replace: boolean }> = [];
		const client = fakeClient({
			createWorkspace: async () => ({ ...record, id: "fresh-cove" }),
			pushArchive: async (id: string, _archive: ReadableStream<Uint8Array>, replace: boolean | undefined) => {
				pushes.push({ id, replace: replace ?? false });
				return { files: 3, bytes: 42 };
			},
			readdir: async () => ["README.md"],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session, {
			inspectLocalDirectory: async () => 3,
			buildLocalArchive: archiveStream,
		});

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.deepEqual(pushes, [{ id: "fresh-cove", replace: false }]);
		assert.ok(harness.confirms[0]?.title.includes("Copy this directory"));
		assert.ok(harness.confirms[0]?.body.includes("3 entries"));
		assert.ok(harness.notices.some((notice) => notice.message.includes("Copied 3 files (42 bytes)")));
	});

	it("skips the offer for an empty local directory", async () => {
		const client = fakeClient({
			createWorkspace: async () => ({ ...record, id: "fresh-cove" }),
			readdir: async () => [],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session, {
			inspectLocalDirectory: async () => 0,
		});

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.deepEqual(harness.confirms, []);
		assert.ok(harness.notices.some((notice) => notice.message.includes("is empty")));
	});

	it("does not push when the offer is declined", async () => {
		let pushed = false;
		const client = fakeClient({
			createWorkspace: async () => ({ ...record, id: "fresh-cove" }),
			pushArchive: async () => {
				pushed = true;
				return { files: 3, bytes: 42 };
			},
			readdir: async () => [],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session, {
			confirmAnswer: false,
			inspectLocalDirectory: async () => 3,
			buildLocalArchive: archiveStream,
		});

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.equal(harness.confirms.length, 1);
		assert.equal(pushed, false);
	});

	it("falls back to the CLI hint when the copy fails", async () => {
		const client = fakeClient({
			createWorkspace: async () => ({ ...record, id: "fresh-cove" }),
			pushArchive: async () => {
				throw new Error("disk full");
			},
			readdir: async () => [],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session, {
			inspectLocalDirectory: async () => 3,
			buildLocalArchive: archiveStream,
		});

		await harness.commands.get("scrap")?.("", harness.ctx);

		const warning = harness.notices.find((notice) => notice.level === "warning");
		assert.ok(warning?.message.includes("scrap push fresh-cove"));
	});

	it("still offers the git clone when a remote exists", async () => {
		const pushes: number[] = [];
		const client = fakeClient({
			createWorkspace: async () => ({ ...record, id: "fresh-cove" }),
			pushArchive: async () => {
				pushes.push(1);
				return { files: 0, bytes: 0 };
			},
			readdir: async () => [],
		});
		const session = new WorkspaceSession(() => client);
		const harness = commandHarness(session, {
			detectRemote: async () => "https://github.com/owner/repo.git",
			inspectLocalDirectory: async () => 3,
			buildLocalArchive: archiveStream,
		});

		await harness.commands.get("scrap")?.("", harness.ctx);

		assert.equal(harness.confirms.length, 1);
		assert.ok(harness.confirms[0]?.title.includes("Clone repository"));
		assert.deepEqual(pushes, []);
	});
});
