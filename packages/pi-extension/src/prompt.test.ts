import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { adaptSystemPrompt, remoteContextBlock } from "./prompt.ts";

const context = {
	workspaceId: "quiet-river",
	project: "owner/project",
	daemonUrl: "http://scrapd.internal:8484",
	root: "/work/project",
	status: "running",
	loadedEnvironment: ["DATABASE_URL"],
	missingEnvironment: ["STRIPE_API_KEY"],
};

describe("remoteContextBlock", () => {
	it("describes the workspace and the fail-closed contract", () => {
		const block = remoteContextBlock(context);
		for (const expected of [
			"Workspace: quiet-river (running)",
			"Project: owner/project",
			"Remote project root: /work/project",
			"Control daemon: http://scrapd.internal:8484",
			"local fallback does not exist",
		]) {
			assert.ok(block.includes(expected), `missing: ${expected}`);
		}
		assert.ok(block.includes("scrap open"));
		assert.ok(block.includes("Approved and loaded: DATABASE_URL"));
		assert.ok(block.includes("Approved but missing at Pi startup: STRIPE_API_KEY"));
		assert.ok(block.includes("scrap env allow NAME"));
		assert.ok(block.includes("Never ask the user"));
	});

	it("tolerates an unknown project", () => {
		const block = remoteContextBlock({ ...context, project: undefined });
		assert.ok(block.includes("Project: unknown"));
	});
});

describe("adaptSystemPrompt", () => {
	it("redirects the working directory line to the remote root", () => {
		const prompt = adaptSystemPrompt(
			"Current working directory: /Users/dev/project\nBe concise.",
			"/Users/dev/project",
			context,
		);
		assert.ok(prompt.includes("Current working directory: /work/project (Scraps remote workspace)"));
		assert.ok(!prompt.includes("/Users/dev/project"));
		assert.ok(prompt.includes("## Scraps remote workspace"));
	});

	it("still appends context when the cwd line is absent", () => {
		const prompt = adaptSystemPrompt("Be concise.", "/Users/dev/project", context);
		assert.ok(prompt.startsWith("Be concise."));
		assert.ok(prompt.includes("## Scraps remote workspace"));
	});
});
