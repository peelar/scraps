import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
	SESSION_BINDING_ENTRY,
	resolveMode,
	resolveSessionMode,
	restoreSessionBinding,
} from "./identity.ts";

describe("resolveMode", () => {
	it("stays local without the --scrap flag", () => {
		const mode = resolveMode({
			flags: {},
			env: { SCRAP_DAEMON_URL: "http://scrapd:8484", SCRAP_WORKSPACE_ID: "quiet-river" },
		});
		assert.deepEqual(mode, { kind: "local" });
	});

	it("enters remote mode with --scrap", () => {
		const mode = resolveMode({
			flags: { scrap: true },
			env: {
				SCRAP_DAEMON_URL: "http://scrapd:8484",
				SCRAP_WORKSPACE_ID: "quiet-river",
				SCRAP_PROJECT: "owner/project",
				SCRAP_TOKEN: "secret",
			},
		});
		assert.equal(mode.kind, "remote");
		if (mode.kind !== "remote") {
			return;
		}
		assert.equal(mode.config.daemonUrl, "http://scrapd:8484");
		assert.equal(mode.config.workspaceId, "quiet-river");
		assert.equal(mode.config.project, "owner/project");
		assert.equal(mode.config.token, "secret");
	});

	it("lets --workspace override SCRAP_WORKSPACE_ID", () => {
		const mode = resolveMode({
			flags: { scrap: true, workspace: "bright-meadow" },
			env: { SCRAP_DAEMON_URL: "http://scrapd:8484", SCRAP_WORKSPACE_ID: "quiet-river" },
		});
		assert.equal(mode.kind, "remote");
		if (mode.kind === "remote") {
			assert.equal(mode.config.workspaceId, "bright-meadow");
		}
	});

	it("defaults the daemon URL when the environment is silent (fail-closed)", () => {
		const mode = resolveMode({ flags: { scrap: true }, env: {} });
		assert.equal(mode.kind, "remote");
		if (mode.kind === "remote") {
			assert.equal(mode.config.daemonUrl, "http://127.0.0.1:8484");
			assert.equal(mode.config.workspaceId, undefined);
		}
	});
});

describe("Pi session workspace bindings", () => {
	const remote = {
		version: 1 as const,
		mode: "remote" as const,
		daemonUrl: "http://worker:8484",
		workspaceId: "quiet-river",
		project: "owner/project",
	};

	it("restores the latest binding on resume", () => {
		const binding = restoreSessionBinding([
			{ type: "custom", customType: SESSION_BINDING_ENTRY, data: remote },
			{ type: "custom", customType: SESSION_BINDING_ENTRY, data: { version: 1, mode: "local" } },
			{ type: "custom", customType: SESSION_BINDING_ENTRY, data: remote },
		]);
		const mode = resolveSessionMode({ flags: {}, env: { SCRAP_TOKEN: "fresh-token" } }, binding);
		assert.equal(mode.kind, "remote");
		if (mode.kind === "remote") {
			assert.equal(mode.config.workspaceId, "quiet-river");
			assert.equal(mode.config.daemonUrl, "http://worker:8484");
			assert.equal(mode.config.token, "fresh-token");
		}
	});

	it("starts a new session locally when it has no binding", () => {
		assert.deepEqual(resolveSessionMode({ flags: {}, env: {} }, undefined), { kind: "local" });
	});

	it("persists local mode after a tossed workspace", () => {
		const binding = restoreSessionBinding([
			{ type: "custom", customType: SESSION_BINDING_ENTRY, data: remote },
			{ type: "custom", customType: SESSION_BINDING_ENTRY, data: { version: 1, mode: "local" } },
		]);
		assert.deepEqual(resolveSessionMode({ flags: {}, env: {} }, binding), { kind: "local" });
	});

	it("does not persist or restore credentials", () => {
		const binding = restoreSessionBinding([{
			type: "custom",
			customType: SESSION_BINDING_ENTRY,
			data: { ...remote, token: "must-not-survive" },
		}]);
		assert.equal("token" in (binding ?? {}), false);
	});
});
