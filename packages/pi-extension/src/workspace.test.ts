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

type FakeHandlers = {
	getWorkspace?: (id: string) => WorkspaceRecord;
};

/** A ScrapdClient double: only the routes a test touches need handlers. */
function fakeClient(handlers: FakeHandlers = {}): ScrapdClient {
	const client = Object.create(ScrapdClient.prototype) as ScrapdClient;
	(client as unknown as { __handlers: FakeHandlers }).__handlers = handlers;
	return client;
}

function patch<Client extends object, Method extends keyof Client & string>(
	client: Client,
	method: Method,
	implementation: Client[Method],
): void {
	Object.defineProperty(client, method, { value: implementation });
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

	it("reports a missing daemon URL as misconfiguration, not local mode", () => {
		const session = new WorkspaceSession();
		session.configure(remoteConfig({ daemonUrl: undefined }));
		assert.throws(() => session.requireClient(), ScrapsUnavailableError);
		assert.equal(statusText(session), "scrap:misconfigured");
	});

	it("is inert before configure", () => {
		const session = new WorkspaceSession();
		assert.equal(session.remoteMode, false);
		assert.equal(statusText(session), undefined);
		assert.ok(describeSession(session).includes("--scrap not given"));
		assert.throws(() => session.requireWorkspace(), ScrapsUnavailableError);
	});
});

describe("WorkspaceSession connection", () => {
	const record: WorkspaceRecord = {
		id: "quiet-river",
		project: "owner/project",
		status: "running",
		root: "/work/project",
	};

	it("shows disconnected status before connecting", () => {
		const session = new WorkspaceSession();
		session.configure(remoteConfig());
		assert.deepEqual(session.connection, { status: "disconnected" });
		assert.equal(statusText(session), "scrap:disconnected");
		assert.equal(session.root, DEFAULT_REMOTE_ROOT);
	});

	it("connects through the daemon and adopts the remote root", async () => {
		const client = fakeClient({ getWorkspace: () => record });
		const session = new WorkspaceSession(() => client);
		session.configure(remoteConfig());

		const connected = await session.connect();

		assert.equal(connected.id, "quiet-river");
		assert.equal(session.connection.status, "connected");
		assert.equal(session.root, "/work/project");
		assert.equal(statusText(session), "scrap:quiet-river");
		assert.ok(describeSession(session).includes("Remote root: /work/project"));
	});

	it("stays disconnected when the workspace is unknown", async () => {
		const client = fakeClient();
		patch(client, "getWorkspace", async () => {
			throw new Error("404: no such workspace");
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
});
