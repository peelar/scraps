import {
	AssistantMessageComponent,
	getMarkdownTheme,
	ToolExecutionComponent,
	UserMessageComponent,
	type ExtensionAPI,
	type ExtensionUIContext,
	type Theme,
} from "@earendil-works/pi-coding-agent";
import { Box, Markdown, Text, truncateToWidth, visibleWidth, type Component, type TUI } from "@earendil-works/pi-tui";

import type { RunEvent, RunRecord } from "./client.ts";
import { ScrapdApiError } from "./errors.ts";
import { describeError, type WorkspaceSession } from "./workspace.ts";

const RUN_BINDING_ENTRY = "scraps-run-v1";
const RUN_USER_ENTRY = "scraps-run-user-v1";
const RUN_STREAM_SLOT_ENTRY = "scraps-run-stream-slot-v1";
const RUN_STREAM_FINAL_ENTRY = "scraps-run-stream-final-v1";
const RUN_WIDGET_KEY = "scrap-run";
const TUI_CAPTURE_WIDGET_KEY = "scrap-tui-capture";

const SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const MAX_MIRRORED_TOOL_OUTPUT_CHARS = 50_000;

type RunBinding = {
	readonly version: 1;
	readonly runId: string;
	readonly workspaceId: string;
	readonly state: RunRecord["state"];
	readonly lastEvent: number;
};

type AssistantBlock =
	| { readonly type: "thinking"; readonly content: string }
	| { readonly type: "text"; readonly content: string };

type AssistantTranscript = {
	readonly kind: "assistant";
	readonly blocks: readonly AssistantBlock[];
	readonly complete: boolean;
	readonly stopReason?: string;
	readonly errorMessage?: string;
};

type ToolTranscript = {
	readonly kind: "tool";
	readonly toolCallId: string;
	readonly toolName: string;
	readonly args: Record<string, unknown>;
	readonly phase: "preparing" | "running" | "done";
	readonly result?: {
		readonly content: readonly Record<string, unknown>[];
		readonly details?: unknown;
		readonly isError: boolean;
	};
	readonly partial?: boolean;
};

export type TranscriptRecord = AssistantTranscript | ToolTranscript;
type SequencedEvent = { sequence: number; data: Record<string, unknown> };
type RunUi = Pick<ExtensionUIContext, "setStatus" | "setWidget" | "notify" | "theme">;
type NativeAssistantMessage = Parameters<AssistantMessageComponent["updateContent"]>[0];

function latestPendingRun(entries: readonly unknown[]): RunBinding | undefined {
	let result: RunBinding | undefined;
	for (const value of entries) {
		if (typeof value !== "object" || value === null) continue;
		const entry = value as { type?: unknown; customType?: unknown; data?: unknown };
		if (entry.type !== "custom" || entry.customType !== RUN_BINDING_ENTRY || typeof entry.data !== "object" || entry.data === null) continue;
		const data = entry.data as Partial<RunBinding>;
		if (data.version !== 1 || typeof data.runId !== "string" || typeof data.workspaceId !== "string" || typeof data.lastEvent !== "number") continue;
		if (data.state === "queued" || data.state === "running") result = data as RunBinding;
		else if (result?.runId === data.runId) result = undefined;
	}
	return result;
}

function sessionNeedsSeed(entries: readonly unknown[], workspaceId: string): boolean {
	return !entries.some((value) => {
		if (typeof value !== "object" || value === null) return false;
		const entry = value as { type?: unknown; customType?: unknown; data?: unknown };
		if (entry.type !== "custom" || entry.customType !== RUN_BINDING_ENTRY || typeof entry.data !== "object" || entry.data === null) return false;
		return (entry.data as { workspaceId?: unknown }).workspaceId === workspaceId;
	});
}

function elapsedLabel(startedAt: number): string {
	const totalSeconds = Math.max(0, Math.floor((Date.now() - startedAt) / 1000));
	if (totalSeconds < 60) return `${totalSeconds}s`;
	const minutes = Math.floor(totalSeconds / 60);
	return `${minutes}m${String(totalSeconds % 60).padStart(2, "0")}s`;
}

function textContent(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.map((part) =>
			typeof part === "object" && part !== null && "type" in part && part.type === "text" && "text" in part
				? String(part.text)
				: "",
		)
		.join("");
}

function record(value: unknown): Record<string, unknown> | undefined {
	return typeof value === "object" && value !== null ? value as Record<string, unknown> : undefined;
}

function contentBlocks(message: unknown): Record<string, unknown>[] {
	const content = record(message)?.content;
	if (!Array.isArray(content)) return [];
	const blocks: Record<string, unknown>[] = [];
	for (const value of content) {
		const block = record(value);
		if (block !== undefined) blocks.push(block);
	}
	return blocks;
}

function assistantFromMessage(message: unknown, complete: boolean): AssistantTranscript | undefined {
	const value = record(message);
	if (value?.role !== "assistant") return undefined;
	const blocks: AssistantBlock[] = [];
	for (const block of contentBlocks(value)) {
		if (block.type === "thinking" && typeof block.thinking === "string" && block.thinking.length > 0) {
			blocks.push({ type: "thinking", content: block.thinking });
		}
		if (block.type === "text" && typeof block.text === "string" && block.text.length > 0) {
			blocks.push({ type: "text", content: block.text });
		}
	}
	return {
		kind: "assistant",
		blocks,
		complete,
		...(typeof value.stopReason === "string" ? { stopReason: value.stopReason } : {}),
		...(typeof value.errorMessage === "string" ? { errorMessage: value.errorMessage } : {}),
	};
}

function boundedToolContent(value: unknown): Record<string, unknown>[] {
	const blocks: Record<string, unknown>[] = [];
	if (Array.isArray(value)) {
		for (const item of value) {
			const block = record(item);
			if (block !== undefined) blocks.push(block);
		}
	}
	let remaining = MAX_MIRRORED_TOOL_OUTPUT_CHARS;
	return blocks.flatMap((block) => {
		if (block.type !== "text" || typeof block.text !== "string") return [block];
		if (remaining <= 0) return [];
		const text = block.text.slice(0, remaining);
		remaining -= text.length;
		return [{ ...block, text: text.length < block.text.length ? `${text}\n… (truncated)` : text }];
	});
}

function toolResult(value: unknown, isError: boolean): NonNullable<ToolTranscript["result"]> {
	const result = record(value) ?? {};
	return {
		content: boundedToolContent(result.content),
		...(result.details === undefined ? {} : { details: result.details }),
		isError,
	};
}

/** Kept as a small, pure compatibility mapper for event-contract tests. */
export type RunArtifact =
	| { kind: "assistant"; text: string }
	| { kind: "thinking"; text: string }
	| { kind: "tool"; toolName: string; isError: boolean; output: string };

export function artifactsFromEvent(event: SequencedEvent): RunArtifact[] {
	if (event.data.type !== "message_end") return [];
	const message = record(event.data.message);
	if (message?.role === "assistant") {
		const assistant = assistantFromMessage(message, true);
		if (assistant === undefined) return [];
		const thinking = assistant.blocks.filter((block) => block.type === "thinking").map((block) => block.content).join("");
		const text = assistant.blocks.filter((block) => block.type === "text").map((block) => block.content).join("");
		return [
			...(thinking.trim().length > 0 ? [{ kind: "thinking" as const, text: thinking }] : []),
			...(text.trim().length > 0 ? [{ kind: "assistant" as const, text }] : []),
		];
	}
	if (message?.role === "toolResult") {
		const toolName = typeof message.toolName === "string" ? message.toolName : "tool";
		const isError = message.isError === true;
		const rawOutput = textContent(message.content);
		const output = rawOutput.length > MAX_MIRRORED_TOOL_OUTPUT_CHARS
			? `${rawOutput.slice(0, MAX_MIRRORED_TOOL_OUTPUT_CHARS)}\n… (truncated)`
			: rawOutput;
		return output.trim().length > 0 || isError ? [{ kind: "tool", toolName, isError, output }] : [];
	}
	return [];
}

export function activityFromEvent(event: SequencedEvent): string | undefined {
	if (event.data.type === "message_update") {
		const update = record(event.data.assistantMessageEvent);
		switch (update?.type) {
			case "thinking_start":
			case "thinking_delta":
				return "thinking";
			case "text_start":
			case "text_delta":
				return "writing";
			case "toolcall_start":
				return typeof update.toolName === "string" ? `preparing ${update.toolName}` : "preparing a tool call";
			case "toolcall_end": {
				const call = record(update.toolCall);
				return typeof call?.name === "string" ? `calling ${call.name}` : undefined;
			}
		}
	}
	if (event.data.type === "tool_execution_start" || event.data.type === "tool_execution_update") {
		return typeof event.data.toolName === "string" ? `running ${event.data.toolName}` : "running a tool";
	}
	if (event.data.type === "tool_execution_end") {
		return typeof event.data.toolName === "string" ? `${event.data.toolName} ${event.data.isError === true ? "failed" : "done"}` : undefined;
	}
	return undefined;
}

function zeroUsage(): NativeAssistantMessage["usage"] {
	return {
		input: 0,
		output: 0,
		cacheRead: 0,
		cacheWrite: 0,
		totalTokens: 0,
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
	};
}

function nativeAssistant(recordValue: AssistantTranscript): NativeAssistantMessage {
	const stopReason = recordValue.stopReason;
	return {
		role: "assistant",
		content: recordValue.blocks.map((block) => block.type === "thinking"
			? { type: "thinking", thinking: block.content }
			: { type: "text", text: block.content }),
		api: "anthropic-messages",
		provider: "scraps",
		model: "remote",
		usage: zeroUsage(),
		stopReason: stopReason === "length" || stopReason === "error" || stopReason === "aborted" || stopReason === "toolUse" || stopReason === "deferred"
			? stopReason
			: recordValue.complete ? "stop" : "pending",
		...(recordValue.errorMessage === undefined ? {} : { errorMessage: recordValue.errorMessage }),
		timestamp: Date.now(),
	};
}

/** Remove the native component's own transcript spacer; custom entries own it. */
function withoutLeadingBlank(lines: string[]): string[] {
	if (lines[0] === undefined || visibleWidth(lines[0]) !== 0) return lines;
	if (lines[1] !== undefined) lines[1] = `${lines[0]}${lines[1]}`; // preserve OSC 133 markers
	return lines.slice(1);
}

class AssistantSlotComponent implements Component {
	private child: AssistantMessageComponent | undefined;
	private previous: AssistantTranscript | undefined;
	private readonly state: () => TranscriptRecord | undefined;
	private readonly expanded: boolean;

	constructor(state: () => TranscriptRecord | undefined, expanded: boolean) {
		this.state = state;
		this.expanded = expanded;
	}

	render(width: number): string[] {
		const value = this.state();
		if (value?.kind !== "assistant" || value.blocks.length === 0) return [];
		if (this.child === undefined) {
			this.child = new AssistantMessageComponent(undefined, !this.expanded, getMarkdownTheme(), "Thinking…", 1);
		}
		if (value !== this.previous) {
			this.child.updateContent(nativeAssistant(value), !value.complete);
			this.previous = value;
		}
		return withoutLeadingBlank(this.child.render(width));
	}

	invalidate(): void {
		this.child?.invalidate();
		this.previous = undefined;
	}
}

class ToolSlotComponent implements Component {
	private child: ToolExecutionComponent | undefined;
	private previous: ToolTranscript | undefined;
	private readonly state: () => TranscriptRecord | undefined;
	private readonly expanded: boolean;
	private readonly getTui: () => TUI | undefined;

	constructor(state: () => TranscriptRecord | undefined, expanded: boolean, getTui: () => TUI | undefined) {
		this.state = state;
		this.expanded = expanded;
		this.getTui = getTui;
	}

	render(width: number): string[] {
		const value = this.state();
		const tui = this.getTui();
		if (value?.kind !== "tool" || tui === undefined) return [];
		if (this.child === undefined) {
			this.child = new ToolExecutionComponent(value.toolName, value.toolCallId, value.args, { showImages: false }, undefined, tui, "/workspace");
		}
		if (value !== this.previous) {
			this.child.updateArgs(value.args);
			if (value.phase !== "preparing") this.child.markExecutionStarted();
			// Do not call setArgsComplete(): the built-in edit renderer would
			// preview against the laptop filesystem. The authoritative remote
			// diff arrives in result.details and renders natively below.
			if (value.result !== undefined) {
				this.child.updateResult({
					content: [...value.result.content] as Array<{ type: string; text?: string; data?: string; mimeType?: string }>,
					details: value.result.details,
					isError: value.result.isError,
				}, value.partial === true);
			}
			this.previous = value;
		}
		this.child.setExpanded(this.expanded);
		return withoutLeadingBlank(this.child.render(width));
	}

	invalidate(): void {
		this.child?.invalidate();
		this.previous = undefined;
	}
}

class RunProgressComponent implements Component {
	private state = "connecting";
	private activity: string | undefined;
	private frame = 0;
	private readonly startedAt = Date.now();
	private readonly timer: ReturnType<typeof setInterval>;
	private readonly tui: TUI;
	private readonly theme: Theme;

	constructor(tui: TUI, theme: Theme) {
		this.tui = tui;
		this.theme = theme;
		this.timer = setInterval(() => {
			this.frame++;
			this.tui.requestRender();
		}, 80);
	}

	update(state: string, activity?: string): void {
		this.state = state;
		this.activity = activity;
		this.tui.requestRender();
	}

	render(width: number): string[] {
		const spinner = SPINNER_FRAMES[this.frame % SPINNER_FRAMES.length] ?? "·";
		const first = this.theme.fg("accent", `${spinner} remote pi · ${this.state} · ${elapsedLabel(this.startedAt)}`);
		const lines = [truncateToWidth(first, width)];
		if (this.activity !== undefined && this.activity !== this.state) {
			lines.push(truncateToWidth(this.theme.fg("muted", `  ${this.activity}`), width));
		}
		return lines;
	}

	invalidate(): void {}

	dispose(): void {
		clearInterval(this.timer);
	}
}

type ProgressController = {
	update: (state: string, activity?: string) => void;
	close: () => void;
};

function showRunProgress(ui: RunUi, onTui: (tui: TUI) => void): ProgressController {
	let component: RunProgressComponent | undefined;
	ui.setWidget(RUN_WIDGET_KEY, (tui, theme) => {
		onTui(tui);
		component = new RunProgressComponent(tui, theme);
		return component;
	});
	return {
		update: (state, activity) => component?.update(state, activity),
		close: () => ui.setWidget(RUN_WIDGET_KEY, undefined),
	};
}

function terminal(state: RunRecord["state"]): boolean {
	return state === "succeeded" || state === "failed" || state === "cancelled";
}

function registerLegacyRenderers(pi: ExtensionAPI): void {
	pi.registerMessageRenderer("scraps-remote-user", (message, { outputPad }) => new UserMessageComponent(textContent(message.content), getMarkdownTheme(), outputPad));
	pi.registerMessageRenderer("scraps-remote-thinking", (message, { expanded, outputPad }, theme) => {
		const text = textContent(message.content);
		return expanded
			? new Markdown(text, outputPad, 0, getMarkdownTheme(), { color: (value) => theme.fg("muted", value), italic: true })
			: new Text(theme.fg("muted", "Thinking…"), outputPad, 0);
	});
	pi.registerMessageRenderer("scraps-remote-tool", (message, { expanded, outputPad }, theme) => {
		const parsed = (() => { try { return JSON.parse(textContent(message.content)) as { toolName?: string; isError?: boolean; output?: string }; } catch { return {}; } })();
		const title = `${theme.fg("toolTitle", `⏺ ${parsed.toolName ?? "tool"}`)} ${parsed.isError ? theme.fg("error", "✗") : theme.fg("success", "✓")}`;
		return new Text(expanded && parsed.output ? `${title}\n${theme.fg("toolOutput", parsed.output)}` : title, outputPad, 0);
	});
	pi.registerMessageRenderer("scraps-remote-assistant", (message, { outputPad }) => new Markdown(textContent(message.content), outputPad, 0, getMarkdownTheme()));
}

/**
 * Route interactive Scraps prompts to the durable runner while rendering the
 * remote JSON event protocol through Pi's own native assistant/tool components.
 */
export function registerDurableRuns(pi: ExtensionAPI, session: WorkspaceSession): void {
	let runnerReadiness: { durableRuns: boolean; modelAuth: boolean; runEventStream: boolean } | undefined;
	let stopped = false;
	let activeAbort: AbortController | undefined;
	let tui: TUI | undefined;

	const knownSlots = new Set<string>();
	const liveSlots = new Map<string, TranscriptRecord>();
	const completedSlots = new Map<string, TranscriptRecord>();

	registerLegacyRenderers(pi);
	pi.registerEntryRenderer(RUN_USER_ENTRY, (entry) => {
		const data = record(entry.data);
		return typeof data?.text === "string" ? new UserMessageComponent(data.text, getMarkdownTheme(), 1) : undefined;
	});
	pi.registerEntryRenderer(RUN_STREAM_SLOT_ENTRY, (entry, { expanded }) => {
		const data = record(entry.data);
		if (typeof data?.slotId !== "string") return undefined;
		const getState = () => liveSlots.get(data.slotId as string) ?? completedSlots.get(data.slotId as string);
		const value = getState();
		if (value?.kind === "tool" || String(data.slotId).includes(":tool:")) {
			return new ToolSlotComponent(getState, expanded, () => tui);
		}
		return new AssistantSlotComponent(getState, expanded);
	});
	// Completion entries are durable update records. Their renderer is
	// intentionally empty; the original slot keeps its chronological position.
	pi.registerEntryRenderer(RUN_STREAM_FINAL_ENTRY, () => undefined);

	const restoreTranscript = (entries: readonly unknown[]) => {
		knownSlots.clear();
		completedSlots.clear();
		for (const value of entries) {
			if (typeof value !== "object" || value === null) continue;
			const entry = value as { type?: unknown; customType?: unknown; data?: unknown };
			const data = record(entry.data);
			if (entry.type !== "custom" || data === undefined || typeof data.slotId !== "string") continue;
			if (entry.customType === RUN_STREAM_SLOT_ENTRY) knownSlots.add(data.slotId);
			if (entry.customType === RUN_STREAM_FINAL_ENTRY && record(data.value)?.kind !== undefined) {
				completedSlots.set(data.slotId, data.value as TranscriptRecord);
			}
		}
	};

	const ensureSlot = (slotId: string, initial: TranscriptRecord) => {
		if (!completedSlots.has(slotId)) liveSlots.set(slotId, initial);
		if (!knownSlots.has(slotId)) {
			knownSlots.add(slotId);
			pi.appendEntry(RUN_STREAM_SLOT_ENTRY, { version: 1, slotId });
		}
		tui?.requestRender();
	};
	const updateSlot = (slotId: string, value: TranscriptRecord) => {
		if (!completedSlots.has(slotId)) liveSlots.set(slotId, value);
		tui?.requestRender();
	};
	const completeSlot = (slotId: string, value: TranscriptRecord) => {
		const alreadyComplete = completedSlots.has(slotId);
		completedSlots.set(slotId, value);
		liveSlots.delete(slotId);
		if (!alreadyComplete) pi.appendEntry(RUN_STREAM_FINAL_ENTRY, { version: 1, slotId, value });
	};

	const readRunnerReadiness = async () => {
		if (runnerReadiness?.durableRuns === true && runnerReadiness.modelAuth === true) return runnerReadiness;
		const features = (await session.requireClient().info()).features;
		runnerReadiness = {
			durableRuns: features?.durableRuns === true,
			modelAuth: features?.modelAuth === true,
			runEventStream: features?.runEventStream === true,
		};
		return runnerReadiness;
	};
	const readinessError = (readiness: { durableRuns: boolean; modelAuth: boolean }): string | undefined => {
		if (!readiness.durableRuns) return "This Scraps worker does not provide the required durable Pi runner. Upgrade or configure the worker before submitting a prompt.";
		if (!readiness.modelAuth) return "The durable Pi runner has no model authorization. On the worker, run: sudo scraps-worker model-auth anthropic";
		return undefined;
	};
	const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

	const follow = async (runId: string, workspaceId: string, after: number, ui: RunUi, progress: ProgressController): Promise<void> => {
		const client = session.requireClient();
		let useStream = runnerReadiness?.runEventStream === true;
		let cursor = after;
		let state: RunRecord["state"] = "queued";
		let activity: string | undefined;
		let backoffMs = 100;
		let activeAssistant: { slotId: string; blocks: Map<number, AssistantBlock> } | undefined;
		const tools = new Map<string, ToolTranscript>();
		const abort = new AbortController();
		activeAbort = abort;

		const assistantValue = (): AssistantTranscript | undefined => activeAssistant === undefined ? undefined : {
			kind: "assistant",
			blocks: [...activeAssistant.blocks.entries()].sort(([a], [b]) => a - b).map(([, block]) => block),
			complete: false,
		};
		const putAssistant = () => {
			const value = assistantValue();
			if (activeAssistant !== undefined && value !== undefined) updateSlot(activeAssistant.slotId, value);
		};
		const putTool = (value: ToolTranscript) => {
			tools.set(value.toolCallId, value);
			const slotId = `${runId}:tool:${value.toolCallId}`;
			if (knownSlots.has(slotId)) updateSlot(slotId, value);
			else ensureSlot(slotId, value);
		};

		const handleEvent = (event: SequencedEvent) => {
			cursor = Math.max(cursor, event.sequence);
			const data = event.data;
			if (data.type === "agent_start") progress.update("working");
			if (data.type === "message_start") {
				const message = record(data.message);
				if (message?.role === "assistant") {
					const slotId = `${runId}:assistant:${event.sequence}`;
					activeAssistant = { slotId, blocks: new Map() };
					ensureSlot(slotId, { kind: "assistant", blocks: [], complete: false });
				}
			}
			if (data.type === "message_update") {
				const update = record(data.assistantMessageEvent);
				const index = typeof update?.contentIndex === "number" ? update.contentIndex : 0;
				if (activeAssistant !== undefined && update !== undefined) {
					if (update.type === "thinking_start") activeAssistant.blocks.set(index, { type: "thinking", content: "" });
					if (update.type === "thinking_delta" && typeof update.delta === "string") {
						const previous = activeAssistant.blocks.get(index);
						activeAssistant.blocks.set(index, { type: "thinking", content: `${previous?.content ?? ""}${update.delta}` });
					}
					if (update.type === "thinking_end" && typeof update.content === "string") activeAssistant.blocks.set(index, { type: "thinking", content: update.content });
					if (update.type === "text_start") activeAssistant.blocks.set(index, { type: "text", content: "" });
					if (update.type === "text_delta" && typeof update.delta === "string") {
						const previous = activeAssistant.blocks.get(index);
						activeAssistant.blocks.set(index, { type: "text", content: `${previous?.content ?? ""}${update.delta}` });
					}
					if (update.type === "text_end" && typeof update.content === "string") activeAssistant.blocks.set(index, { type: "text", content: update.content });
					putAssistant();
				}
				if (update?.type === "toolcall_start" && typeof update.id === "string") {
					putTool({ kind: "tool", toolCallId: update.id, toolName: typeof update.toolName === "string" ? update.toolName : "tool", args: {}, phase: "preparing" });
				}
				if (update?.type === "toolcall_end") {
					const call = record(update.toolCall);
					if (typeof call?.id === "string") {
						const previous = tools.get(call.id);
						putTool({
							kind: "tool",
							toolCallId: call.id,
							toolName: typeof call.name === "string" ? call.name : previous?.toolName ?? "tool",
							args: record(call.arguments) ?? {},
							phase: "preparing",
						});
					}
				}
			}
			if (data.type === "message_end") {
				const message = record(data.message);
				if (message?.role === "assistant") {
					const value = assistantFromMessage(message, true);
					if (value !== undefined) {
						if (activeAssistant === undefined) {
							const slotId = `${runId}:assistant:${event.sequence}`;
							ensureSlot(slotId, value);
							completeSlot(slotId, value);
						} else {
							completeSlot(activeAssistant.slotId, value);
						}
					}
					activeAssistant = undefined;
				}
				if (message?.role === "toolResult" && typeof message.toolCallId === "string") {
					const previous = tools.get(message.toolCallId);
					if (previous?.phase !== "done") {
						const value: ToolTranscript = {
							kind: "tool",
							toolCallId: message.toolCallId,
							toolName: typeof message.toolName === "string" ? message.toolName : previous?.toolName ?? "tool",
							args: previous?.args ?? {},
							phase: "done",
							result: { content: boundedToolContent(message.content), isError: message.isError === true },
						};
						putTool(value);
						completeSlot(`${runId}:tool:${value.toolCallId}`, value);
					}
				}
			}
			if (data.type === "tool_execution_start" && typeof data.toolCallId === "string") {
				const previous = tools.get(data.toolCallId);
				putTool({
					kind: "tool",
					toolCallId: data.toolCallId,
					toolName: typeof data.toolName === "string" ? data.toolName : previous?.toolName ?? "tool",
					args: record(data.args) ?? previous?.args ?? {},
					phase: "running",
				});
			}
			if (data.type === "tool_execution_update" && typeof data.toolCallId === "string") {
				const previous = tools.get(data.toolCallId);
				putTool({
					kind: "tool",
					toolCallId: data.toolCallId,
					toolName: typeof data.toolName === "string" ? data.toolName : previous?.toolName ?? "tool",
					args: record(data.args) ?? previous?.args ?? {},
					phase: "running",
					result: toolResult(data.partialResult, false),
					partial: true,
				});
			}
			if (data.type === "tool_execution_end" && typeof data.toolCallId === "string") {
				const previous = tools.get(data.toolCallId);
				const value: ToolTranscript = {
					kind: "tool",
					toolCallId: data.toolCallId,
					toolName: typeof data.toolName === "string" ? data.toolName : previous?.toolName ?? "tool",
					args: previous?.args ?? {},
					phase: "done",
					result: toolResult(data.result, data.isError === true),
				};
				putTool(value);
				completeSlot(`${runId}:tool:${value.toolCallId}`, value);
			}
			const nextActivity = activityFromEvent(event);
			if (nextActivity !== undefined) activity = nextActivity;
			progress.update(state === "queued" ? "working" : state, activity);
		};

		try {
			while (!stopped) {
				if (useStream) {
					try {
						await client.streamEvents(runId, cursor, handleEvent, abort.signal);
					} catch (error) {
						if (stopped || abort.signal.aborted) return;
						if (error instanceof ScrapdApiError && (error.status === 404 || error.status === 405)) {
							runnerReadiness = { ...(runnerReadiness ?? { durableRuns: true, modelAuth: true }), runEventStream: false };
							useStream = false;
							continue;
						}
						progress.update("reconnecting", activity);
						await sleep(backoffMs);
						backoffMs = Math.min(backoffMs * 2, 2_000);
						continue;
					}
					backoffMs = 100;
				} else {
					const events = await client.runEvents(runId, cursor);
					for (const event of events) handleEvent(event);
					await sleep(250);
				}

				const run = await client.getRun(runId);
				state = run.state;
				if (terminal(run.state)) {
					const finalEvents = await client.runEvents(runId, cursor);
					for (const event of finalEvents) handleEvent(event);
					pi.appendEntry(RUN_BINDING_ENTRY, { version: 1, runId, workspaceId, state: run.state, lastEvent: cursor } satisfies RunBinding);
					if (run.state !== "succeeded") ui.notify(`Remote Pi run ${run.state}: ${run.error ?? "unknown error"}`, "error");
					ui.setStatus("scrap-run", undefined);
					return;
				}
				pi.appendEntry(RUN_BINDING_ENTRY, { version: 1, runId, workspaceId, state: run.state, lastEvent: cursor } satisfies RunBinding);
				ui.setStatus("scrap-run", ui.theme.fg("accent", `run:${run.state}`));
			}
		} finally {
			if (activeAbort === abort) activeAbort = undefined;
		}
	};

	pi.on("session_start", async (_event, ctx) => {
		stopped = false;
		restoreTranscript(ctx.sessionManager.getBranch());
		// Capture the public TUI handle once. Native tool components need it for
		// their own invalidation, but no persistent widget is required.
		ctx.ui.setWidget(TUI_CAPTURE_WIDGET_KEY, (nextTui) => {
			tui = nextTui;
			return { render: () => [], invalidate: () => {} };
		});
		ctx.ui.setWidget(TUI_CAPTURE_WIDGET_KEY, undefined);
		if (!session.remoteMode) return;
		try {
			const readiness = await readRunnerReadiness();
			const problem = readinessError(readiness);
			if (problem !== undefined) {
				ctx.ui.setStatus("scrap-run", readiness.durableRuns ? "runner:unauthorized" : "runner:unavailable");
				ctx.ui.notify(problem, "error");
				return;
			}
		} catch (error) {
			ctx.ui.setStatus("scrap-run", "runner:disconnected");
			ctx.ui.notify(`Cannot verify the durable Pi runner: ${describeError(error)}`, "error");
			return;
		}
		const pending = latestPendingRun(ctx.sessionManager.getBranch());
		if (pending === undefined || pending.workspaceId !== session.workspaceId) return;
		const progress = showRunProgress(ctx.ui, (nextTui) => { tui = nextTui; });
		progress.update("reconnecting");
		void follow(pending.runId, pending.workspaceId, pending.lastEvent, ctx.ui, progress)
			.catch((error) => {
				ctx.ui.setStatus("scrap-run", "run:detached");
				ctx.ui.notify(`Remote run continues, but its event stream is unavailable: ${describeError(error)}`, "warning");
			})
			.finally(progress.close);
	});

	pi.on("session_shutdown", () => {
		stopped = true;
		activeAbort?.abort();
	});

	pi.registerCommand("scrap-cancel", {
		description: "Cancel the active durable Pi run",
		handler: async (_args, ctx) => {
			if (!session.remoteMode) {
				ctx.ui.notify("Not in Scraps mode.", "warning");
				return;
			}
			const pending = latestPendingRun(ctx.sessionManager.getBranch());
			if (pending === undefined || pending.workspaceId !== session.workspaceId) {
				ctx.ui.notify("No active durable Pi run in this session.", "info");
				return;
			}
			try {
				await session.requireClient().cancelRun(pending.runId);
				ctx.ui.setStatus("scrap-run", "run:cancelling");
				ctx.ui.notify(`Cancelling remote Pi run ${pending.runId}…`, "info");
			} catch (error) {
				ctx.ui.notify(`Could not cancel remote Pi run: ${describeError(error)}`, "error");
			}
		},
	});

	pi.on("input", async (event, ctx) => {
		if (process.env.SCRAP_DURABLE_RUN === "1") return;
		if (!session.remoteMode || session.connectedWorkspace === undefined || event.source !== "interactive") return;

		const workspaceId = session.connectedWorkspace.id;
		const sessionKey = ctx.sessionManager.getSessionId();
		const branch = ctx.sessionManager.getBranch();
		// Match vanilla Pi: echo the prompt and show activity synchronously,
		// before either the create request or runner startup can add latency.
		pi.appendEntry(RUN_USER_ENTRY, { version: 1, text: event.text });
		const progress = showRunProgress(ctx.ui, (nextTui) => { tui = nextTui; });
		try {
			const readiness = await readRunnerReadiness();
			const problem = readinessError(readiness);
			if (problem !== undefined) {
				ctx.ui.setStatus("scrap-run", readiness.durableRuns ? "runner:unauthorized" : "runner:unavailable");
				ctx.ui.notify(`Prompt not started: ${problem}`, "error");
				return { action: "handled" as const };
			}
			let sessionSnapshot: Record<string, unknown>[] = [];
			if (sessionNeedsSeed(branch, workspaceId)) {
				const contextEntryTypes = new Set(["message", "model_change", "thinking_level_change", "compaction", "branch_summary", "custom_message", "session_info"]);
				let importedParent: string | null = null;
				const importedBranch: Record<string, unknown>[] = [];
				for (const value of branch) {
					if (typeof value !== "object" || value === null) continue;
					const entry = value as unknown as Record<string, unknown>;
					if (typeof entry.type !== "string" || !contextEntryTypes.has(entry.type) || typeof entry.id !== "string") continue;
					importedBranch.push({ ...entry, parentId: importedParent });
					importedParent = entry.id;
				}
				sessionSnapshot = [
					{ type: "scraps_checkpoint", version: 1, provider: ctx.model?.provider, model: ctx.model?.id, thinkingLevel: ctx.thinkingLevel },
					...importedBranch,
				];
			}
			progress.update("starting");
			const run = await session.requireClient().createRun(workspaceId, event.text, sessionKey, sessionSnapshot);
			pi.appendEntry(RUN_BINDING_ENTRY, { version: 1, runId: run.id, workspaceId, state: run.state, lastEvent: 0 } satisfies RunBinding);
			ctx.ui.setStatus("scrap-run", `run:${run.state}`);
			await follow(run.id, workspaceId, 0, ctx.ui, progress);
		} catch (error) {
			ctx.ui.setStatus("scrap-run", "run:detached");
			ctx.ui.notify(`Cannot follow durable Pi run: ${describeError(error)}. If it was accepted, it continues on the worker.`, "warning");
		} finally {
			progress.close();
		}
		return { action: "handled" as const };
	});
}
