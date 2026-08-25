import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { resolveMode } from "./identity.ts";

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
