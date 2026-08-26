import assert from "node:assert/strict";
import { describe, it } from "node:test";

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { type WorkspaceRecord, ScrapdClient } from "./client.ts";
import { registerScrapCommands } from "./commands.ts";
import type { RemoteConfig, SessionBinding } from "./identity.ts";
import { WorkspaceSession, statusText } from "./workspace.ts";

type CommandHandler = (args: string, ctx: any) => Promise<void>;

function commandHarness(session: WorkspaceSession) {
	const commands = new Map<string, CommandHandler>();
	const statuses: Array<string | undefined> = [];
	const notices: Array<{ message: string; level: string }> = [];
	const bindings: SessionBinding[] = [];
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
	);
	const ctx = {
		cwd: "/tmp/project",
		ui: {
			notify: (message: string, level: string) => notices.push({ message, level }),
			setStatus: () => {},
			confirm: async () => true,
		},
	};
	return { commands, statuses, notices, bindings, ctx };
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
