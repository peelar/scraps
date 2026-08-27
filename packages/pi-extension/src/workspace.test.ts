import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { ScrapsUnavailableError } from "./errors.ts";
import type { RemoteConfig } from "./identity.ts";
import { type WorkspaceRecord, ScrapdClient } from "./client.ts";
import {
	DEFAULT_REMOTE_ROOT,
	DISCONNECTED_TOOL_MESSAGE,
	WorkspaceSession,
	describeSession,
	statusText,
} from "./workspace.ts";

function remoteConfig(overrides: Partial<RemoteConfig> = {}): RemoteConfig {
	return {
		daemonUrl: "http://127.0.0.1:1",
		workspaceId: "quiet-river",
		project: "owner/project",
		token: undefined,
		...overrides,
	};
}

type FakeMethods = {
	getWorkspace?: (id: string) => Promise<WorkspaceRecord>;
	createWorkspace?: (input: unknown) => Promise<WorkspaceRecord>;
	info?: () => Promise<{ name: string; version: string; provider?: string }>;
	ports?: (id: string) => Promise<number[]>;
};

/** A ScrapdClient double exposing only the routes a test needs. */
function fakeClient(methods: FakeMethods): ScrapdClient {
	return Object.assign(Object.create(ScrapdClient.prototype), {
		info: async () => ({ name: "scrapd", version: "test", provider: "docker" }),
		ports: async () => [] as number[],
		...methods,
	}) as ScrapdClient;
}

describe("WorkspaceSession (fail-closed)", () => {
	it("requires a workspace before any tool operation", () => {
		const session = new WorkspaceSession();
		session.configure(remoteConfig());
		try {
			session.requireWorkspace();
			assert.fail("expected requireWorkspace to throw");
		} catch (error) {
			assert.ok(error instanceof ScrapsUnavailableError);
			assert.ok(error.message.includes(DISCONNECTED_TOOL_MESSAGE));
		}
	});

	it("is inert before configure", () => {
		const session = new WorkspaceSession();
		assert.equal(session.remoteMode, false);
		assert.equal(statusText(session), undefined);
		assert.ok(describeSession(session).includes("project tools are local"));
		assert.throws(() => session.requireWorkspace(), ScrapsUnavailableError);
	});
});

describe("WorkspaceSession connection", () => {
	const record: WorkspaceRecord = {
		id: "quiet-river",
		project: "owner/project",
		state: "running",
		rootPath: "/workspace",
		pathContract: "workspace-relative-v1",
	};

	it("shows disconnected status before connecting", () => {
		const session = new WorkspaceSession();
		session.configure(remoteConfig());
		assert.deepEqual(session.connection, { status: "disconnected" });
		assert.equal(statusText(session), "scrap:disconnected");
		assert.equal(session.root, DEFAULT_REMOTE_ROOT);
	});

	it("connects through the daemon using the stable virtual root", async () => {
		const client = fakeClient({ getWorkspace: async () => record });
		const session = new WorkspaceSession(() => client);
		session.configure(remoteConfig());

		const connected = await session.connect();

		assert.equal(connected.id, "quiet-river");
		assert.equal(session.connection.status, "connected");
		assert.equal(session.root, "/workspace");
		assert.equal(statusText(session), "scrap:docker:quiet-river:running");
		assert.ok(describeSession(session).includes("Provider: docker"));
		assert.ok(describeSession(session).includes("Remote root: /workspace"));
	});

	it("creates workspaces with an optional repository to clone", async () => {
		const inputs: unknown[] = [];
		const client = fakeClient({
			createWorkspace: async (input: unknown) => {
				inputs.push(input);
				return record;
			},
		});
		const session = new WorkspaceSession(() => client);
		session.configure(remoteConfig());

		await session.create({ project: "demo", repoUrl: "https://github.com/peelar/scraps.git" });
		await session.create();

		assert.deepEqual(inputs, [
			{ project: "demo", repoUrl: "https://github.com/peelar/scraps.git" },
			{},
		]);
		assert.equal(session.connection.status, "connected");
	});

	it("fails explicitly for the transitional host-path contract", async () => {
		const client = fakeClient({
			getWorkspace: async () => ({
				id: record.id,
				state: record.state,
				rootPath: "/srv/workspaces/quiet-river",
			}),
		});
		const session = new WorkspaceSession(() => client);
		session.configure(remoteConfig());

		await assert.rejects(() => session.connect(), /Incompatible scrapd path contract/);
		assert.equal(session.connection.status, "disconnected");
	});

	it("stays disconnected when the workspace is unknown", async () => {
		const client = fakeClient({
			getWorkspace: async () => {
				throw new Error("404: no such workspace");
			},
		});
		const session = new WorkspaceSession(() => client);
		session.configure(remoteConfig());

		await assert.rejects(() => session.connect());

		assert.equal(session.connection.status, "disconnected");
		assert.equal(statusText(session), "scrap:disconnected");
		assert.throws(() => session.requireWorkspace(), ScrapsUnavailableError);
	});

	it("requires a workspace id to connect", async () => {
		const session = new WorkspaceSession();
		session.configure(remoteConfig({ workspaceId: undefined }));
		await assert.rejects(() => session.connect(), ScrapsUnavailableError);
	});

	it("returns to local mode only after explicit deactivation", async () => {
		const client = fakeClient({ getWorkspace: async () => record });
		const session = new WorkspaceSession(() => client);
		session.configure(remoteConfig());
		await session.connect();

		session.deactivate();

		assert.equal(session.remoteMode, false);
		assert.equal(session.connectedWorkspace, undefined);
		assert.equal(statusText(session), undefined);
	});
});

describe("WorkspaceSession port hints", () => {
	const record: WorkspaceRecord = {
		id: "quiet-river",
		project: "owner/project",
		state: "running",
		rootPath: "/workspace",
		pathContract: "workspace-relative-v1",
	};

	function connectedSession(ports: number[]) {
		const client = fakeClient({
			getWorkspace: async () => record,
			ports: async () => [...ports],
		});
		const session = new WorkspaceSession(() => client);
		session.configure(remoteConfig());
		return { session, apply: (next: number[]) => void (ports = next) };
	}

	it("notifies once for newly listening ports and prunes gone ones", async () => {
		const { session, apply } = connectedSession([]);
		await session.connect();
		const notified: number[][] = [];
		session.setPortsListener((ports) => notified.push(ports));

		apply([5173]);
		await session.refreshPorts();
		apply([5173]);
		await session.refreshPorts();
		assert.deepEqual(notified, [[5173]]);

		// A second port appears: only the newcomer is reported.
		apply([3000, 5173]);
		await session.refreshPorts();
		assert.deepEqual(notified, [[5173], [3000]]);

		// The server stops and restarts: the port is new again.
		apply([]);
		await session.refreshPorts();
		apply([5173]);
		await session.refreshPorts();
		assert.deepEqual(notified, [[5173], [3000], [5173]]);
	});

	it("seeds ports on connect without notifying", async () => {
		const { session } = connectedSession([5173]);
		const notified: number[][] = [];
		await session.connect();
		session.setPortsListener((ports) => notified.push(ports));
		await session.refreshPorts();
		assert.deepEqual(notified, []);
		assert.deepEqual(session.listeningPorts, [5173]);
		assert.ok(statusText(session)?.includes(":5173"));
	});

	it("shows no ports in the status line when nothing listens", async () => {
		const { session } = connectedSession([]);
		await session.connect();
		assert.equal(statusText(session), "scrap:docker:quiet-river:running");
	});

	it("swallows port discovery failures", async () => {
		const { session } = connectedSession([]);
		await session.connect();
		const client = fakeClient({
			getWorkspace: async () => record,
			ports: async () => {
				throw new Error("daemon unreachable");
			},
		});
		// Private field access: replace the wired client for this assertion.
		session["client"] = client;
		await session.refreshPorts();
		assert.deepEqual(session.listeningPorts, []);
	});
});
