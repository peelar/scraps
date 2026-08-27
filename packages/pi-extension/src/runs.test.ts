import assert from "node:assert/strict";
import { test } from "node:test";

import { activityFromEvent, artifactsFromEvent } from "./runs.ts";
import type { RunEvent } from "./client.ts";

const event = (sequence: number, data: unknown): RunEvent => ({
	sequence,
	data: data as Record<string, unknown>,
	createdAt: new Date().toISOString(),
});

test("assistant message_end mirrors thinking and text separately, in order", () => {
	const artifacts = artifactsFromEvent(
		event(3, {
			type: "message_end",
			message: {
				role: "assistant",
				content: [
					{ type: "thinking", thinking: "Let me check the spec." },
					{ type: "text", text: "Phase 0 is the spike." },
					{ type: "toolCall", id: "t1", name: "read", arguments: {} },
				],
			},
		}),
	);
	assert.deepEqual(artifacts, [
		{ kind: "thinking", text: "Let me check the spec." },
		{ kind: "assistant", text: "Phase 0 is the spike." },
	]);
});

test("blank assistant content mirrors nothing", () => {
	const artifacts = artifactsFromEvent(
		event(4, { type: "message_end", message: { role: "assistant", content: [{ type: "text", text: "  " }] } }),
	);
	assert.deepEqual(artifacts, []);
});

test("toolResult message_end mirrors tool name, failure, and truncated output", () => {
	const long = "x".repeat(9_000);
	const artifacts = artifactsFromEvent(
		event(5, {
			type: "message_end",
			message: {
				role: "toolResult",
				toolName: "bash",
				isError: true,
				content: [{ type: "text", text: long }],
			},
		}),
	);
	assert.equal(artifacts.length, 1);
	const [artifact] = artifacts;
	assert.ok(artifact, "expected one artifact");
	assert.equal(artifact.kind, "tool");
	if (artifact.kind !== "tool") return;
	assert.equal(artifact.toolName, "bash");
	assert.equal(artifact.isError, true);
	assert.ok(artifact.output.length < 9_000);
	assert.ok(artifact.output.endsWith("… (truncated)"));
});

test("non-message events mirror nothing", () => {
	assert.deepEqual(artifactsFromEvent(event(6, { type: "agent_start" })), []);
	assert.deepEqual(artifactsFromEvent(event(7, { type: "turn_end", message: { role: "assistant", content: [{ type: "text", text: "dup" }] } })), []);
	assert.deepEqual(artifactsFromEvent(event(8, { type: "message_end", message: { role: "user", content: "hi" } })), []);
});

test("thinking deltas drive the activity line", () => {
	assert.equal(activityFromEvent(event(9, { type: "message_update", assistantMessageEvent: { type: "thinking_delta", delta: "hmm" } })), "thinking");
	assert.equal(activityFromEvent(event(10, { type: "message_update", assistantMessageEvent: { type: "text_delta", delta: "hello wor" } })), "writing: hello wor");
});

test("tool lifecycle drives the activity line", () => {
	assert.equal(activityFromEvent(event(11, { type: "tool_execution_start", toolName: "bash" })), "running bash");
	assert.equal(activityFromEvent(event(12, { type: "tool_execution_end", toolName: "bash", isError: false })), "bash done");
	assert.equal(activityFromEvent(event(13, { type: "tool_execution_end", toolName: "bash", isError: true })), "bash failed");
	assert.equal(activityFromEvent(event(14, { type: "message_update", assistantMessageEvent: { type: "toolcall_start", toolName: "edit" } })), "preparing edit");
	assert.equal(activityFromEvent(event(15, { type: "agent_settled" })), undefined);
});
