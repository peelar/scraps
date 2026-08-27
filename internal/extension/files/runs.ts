import type { ExtensionAPI, ThemeColor } from "@earendil-works/pi-coding-agent";
import { getMarkdownTheme } from "@earendil-works/pi-coding-agent";
import { Box, Markdown, Text } from "@earendil-works/pi-tui";

import type { RunEvent, RunRecord } from "./client.ts";
import { ScrapdApiError } from "./errors.ts";
import { describeError, type WorkspaceSession } from "./workspace.ts";

const RUN_BINDING_ENTRY = "scraps-run-v1";

type RunBinding = {
	readonly version: 1;
	readonly runId: string;
	readonly workspaceId: string;
	readonly state: RunRecord["state"];
	readonly lastEvent: number;
};

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

/** Spinner frames for the live run indicator; native-like braille rotation. */
const SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const RUN_WIDGET_KEY = "scrap-run";

/** UI surface follow() needs; satisfied by the interactive extension context. */
type RunUi = {
	setStatus: (key: string, text: string | undefined) => void;
	setWidget: (key: string, lines: string[] | undefined) => void;
	notify: (message: string, level: "info" | "warning" | "error") => void;
	theme: { fg: (color: ThemeColor, text: string) => string };
};

function elapsedLabel(startedAt: number): string {
	const totalSeconds = Math.max(0, Math.floor((Date.now() - startedAt) / 1000));
	if (totalSeconds < 60) return `${totalSeconds}s`;
	const minutes = Math.floor(totalSeconds / 60);
	return `${minutes}m${String(totalSeconds % 60).padStart(2, "0")}s`;
}

/** Custom message content arrives as a string or content blocks; flatten it. */
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

/**
 * Transcript artifacts mirrored from the remote run. Tool calls and thinking
 * render as their own transcript rows so a remote run reads like a local one.
 */
export type RunArtifact =
	| { kind: "assistant"; text: string }
	| { kind: "thinking"; text: string }
	| { kind: "tool"; toolName: string; isError: boolean; output: string };

const MAX_MIRRORED_TOOL_OUTPUT_CHARS = 8_000;

function contentBlocks(message: unknown): { type: string; text?: string; thinking?: string }[] {
	if (typeof message !== "object" || message === null) return [];
	const content = (message as { content?: unknown }).content;
	return Array.isArray(content) ? (content as { type: string }[]) : [];
}

/** Minimal shape shared by polled RunEvents and streamed envelopes. */
type SequencedEvent = { sequence: number; data: Record<string, unknown> };

/**
 * Map one persisted run event to the transcript artifacts it completes.
 * Streaming deltas intentionally produce nothing here — they only feed the
 * live activity indicator until the authoritative `message_end` lands.
 */
export function artifactsFromEvent(event: SequencedEvent): RunArtifact[] {
	if (event.data.type !== "message_end") return [];
	const message = event.data.message;
	if (typeof message !== "object" || message === null) return [];
	const role = (message as { role?: unknown }).role;
	if (role === "assistant") {
		const artifacts: RunArtifact[] = [];
		let thinking = "";
		let text = "";
		for (const block of contentBlocks(message)) {
			if (block.type === "thinking" && typeof block.thinking === "string") thinking += block.thinking;
			if (block.type === "text" && typeof block.text === "string") text += block.text;
		}
		if (thinking.trim().length > 0) artifacts.push({ kind: "thinking", text: thinking });
		if (text.trim().length > 0) artifacts.push({ kind: "assistant", text });
		return artifacts;
	}
	if (role === "toolResult") {
		const toolName = typeof (message as { toolName?: unknown }).toolName === "string" ? (message as { toolName: string }).toolName : "tool";
		const isError = (message as { isError?: unknown }).isError === true;
		let output = contentBlocks(message).map((block) => (block.type === "text" && typeof block.text === "string" ? block.text : "")).join("");
		if (output.length > MAX_MIRRORED_TOOL_OUTPUT_CHARS) output = `${output.slice(0, MAX_MIRRORED_TOOL_OUTPUT_CHARS)}\n… (truncated)`;
		if (output.trim().length > 0 || isError) return [{ kind: "tool", toolName, isError, output }];
		return [];
	}
	return [];
}

/** One-line status of what the remote agent is doing right now, if anything. */
export function activityFromEvent(event: SequencedEvent): string | undefined {
	if (event.data.type === "message_update") {
		const update = event.data.assistantMessageEvent;
		if (typeof update !== "object" || update === null) return undefined;
		switch ((update as { type?: unknown }).type) {
			case "thinking_start":
			case "thinking_delta":
				return "thinking";
			case "text_start":
			case "text_delta": {
				const delta = (update as { delta?: unknown }).delta;
				const tail = typeof delta === "string" ? delta.trimEnd().split("\n").pop() ?? "" : "";
				return tail.length > 0 ? `writing: ${tail.slice(-48)}` : "writing";
			}
			case "toolcall_start": {
				const name = (update as { toolName?: unknown }).toolName;
				return typeof name === "string" ? `preparing ${name}` : "preparing a tool call";
			}
			case "toolcall_end": {
				const call = (update as { toolCall?: { name?: unknown } }).toolCall;
				return typeof call?.name === "string" ? `calling ${call.name}` : undefined;
			}
			default:
				return undefined;
		}
	}
	if (event.data.type === "tool_execution_start" || event.data.type === "tool_execution_update") {
		const name = event.data.toolName;
		return typeof name === "string" ? `running ${name}` : "running a tool";
	}
	if (event.data.type === "tool_execution_end") {
		const name = event.data.toolName;
		const failed = event.data.isError === true;
		return typeof name === "string" ? `${name} ${failed ? "failed" : "done"}` : undefined;
	}
	return undefined;
}

/**
 * Render remote mirrors distinctly from local traffic: the user echo is
 * muted, thinking is a dim collapsed trace, tool calls are single status
 * rows, and the assistant reply gets native-style markdown.
 */
function registerRunRenderers(pi: ExtensionAPI): void {
	pi.registerMessageRenderer("scraps-remote-user", (message, { outputPad }, theme) => {
		const text = [
			theme.fg("muted", "[remote user]"),
			"",
			theme.fg("userMessageText", textContent(message.content)),
		].join("\n");
		return new Text(text, outputPad, 0);
	});
	pi.registerMessageRenderer("scraps-remote-thinking", (message, { expanded, outputPad }, theme) => {
		const text = textContent(message.content);
		if (expanded) {
			return new Text([theme.fg("muted", "[remote thinking]"), "", theme.fg("muted", text)].join("\n"), outputPad, 0);
		}
		const characters = text.length.toLocaleString("en-US");
		return new Text(theme.fg("muted", `· thinking · ${characters} chars ·`), outputPad, 0);
	});
	pi.registerMessageRenderer("scraps-remote-tool", (message, { expanded, outputPad }, theme) => {
		let toolName = "tool";
		let isError = false;
		let output = "";
		try {
			const parsed = JSON.parse(textContent(message.content)) as { toolName?: unknown; isError?: unknown; output?: unknown };
			if (typeof parsed.toolName === "string") toolName = parsed.toolName;
			if (typeof parsed.isError === "boolean") isError = parsed.isError;
			if (typeof parsed.output === "string") output = parsed.output;
		} catch {
			// fall through with defaults
		}
		const status = isError ? theme.fg("error", "✗") : theme.fg("success", "✓");
		if (!expanded || output.length === 0) {
			return new Text(`${theme.fg("accent", `⏺ ${toolName}`)} ${status}`, outputPad, 0);
		}
		const lines = output.split("\n").slice(0, 200).map((line) => theme.fg("muted", `  ${line}`));
		return new Text([`${theme.fg("accent", `⏺ ${toolName}`)} ${status}`, ...lines].join("\n"), outputPad, 0);
	});
	pi.registerMessageRenderer("scraps-remote-assistant", (message, { outputPad }, theme) => {
		const box = new Box(outputPad, 0);
		box.addChild(new Text(theme.fg("accent", "[remote assistant]"), 0, 0));
		box.addChild(new Text("", 0, 0));
		box.addChild(new Markdown(textContent(message.content), outputPad, 0, getMarkdownTheme()));
		return box;
	});
}

function terminal(state: RunRecord["state"]): boolean {
	return state === "succeeded" || state === "failed" || state === "cancelled";
}

/**
 * Route every interactive Scraps prompt to scrapd's durable Pi runner.
 * Durable execution is the `/scrap` contract; unsupported workers fail closed
 * instead of silently returning ownership of the agent loop to the laptop.
 */
export function registerDurableRuns(pi: ExtensionAPI, session: WorkspaceSession): void {
	let runnerReadiness: { durableRuns: boolean; modelAuth: boolean; runEventStream: boolean } | undefined;
	let stopped = false;
	let activeAbort: AbortController | undefined;

	registerRunRenderers(pi);

	const readRunnerReadiness = async (): Promise<{ durableRuns: boolean; modelAuth: boolean; runEventStream: boolean }> => {
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

	/**
	 * Follow a run until it settles. Events stream over SSE the moment the
	 * worker persists them (falling back to polling on older daemons); the
	 * spinner and elapsed timer animate locally so the UI never stutters
	 * regardless of network latency.
	 */
	const follow = async (runId: string, workspaceId: string, after: number, ui: RunUi): Promise<void> => {
		const client = session.requireClient();
		let useStream = runnerReadiness?.runEventStream === true;
		let cursor = after;
		const startedAt = Date.now();
		let frameIndex = 0;
		let state = "starting";
		let activity: string | undefined;
		let backoffMs = 250;

		const renderWidget = () => {
			const dot = SPINNER_FRAMES[frameIndex++ % SPINNER_FRAMES.length];
			const lines = [ui.theme.fg("accent", `${dot} remote pi · ${state} · ${elapsedLabel(startedAt)}`)];
			if (activity !== undefined) lines.push(ui.theme.fg("muted", `  ${activity}`));
			ui.setWidget(RUN_WIDGET_KEY, lines);
		};
		const animation = setInterval(renderWidget, 100);
		const abort = new AbortController();
		activeAbort = abort;

		const handleEvent = (event: SequencedEvent) => {
			cursor = Math.max(cursor, event.sequence);
			for (const artifact of artifactsFromEvent(event)) {
				if (artifact.kind === "assistant") {
					pi.sendMessage({ customType: "scraps-remote-assistant", content: artifact.text, display: true });
				} else if (artifact.kind === "thinking") {
					pi.sendMessage({ customType: "scraps-remote-thinking", content: artifact.text, display: true });
				} else {
					pi.sendMessage({
						customType: "scraps-remote-tool",
						content: JSON.stringify({ toolName: artifact.toolName, isError: artifact.isError, output: artifact.output }),
						display: true,
					});
				}
			}
			const nextActivity = activityFromEvent(event);
			if (nextActivity !== undefined) activity = nextActivity;
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
						// Connection dropped mid-run; the run continues on the
						// worker, so reconnect from the last seen event.
						await sleep(backoffMs);
						backoffMs = Math.min(backoffMs * 2, 2_000);
						continue;
					}
					backoffMs = 250;
				} else {
					const events = await client.runEvents(runId, cursor);
					for (const event of events) handleEvent(event);
					await sleep(400);
					if (stopped) return;
				}

				const run = await client.getRun(runId);
				state = run.state;
				if (terminal(run.state)) {
					// The terminal state is persisted after the executor's final
					// event; sweep once more so nothing is missed.
					const finalEvents = await client.runEvents(runId, cursor);
					for (const event of finalEvents) handleEvent(event);
					pi.appendEntry(RUN_BINDING_ENTRY, { version: 1, runId, workspaceId, state: run.state, lastEvent: cursor } satisfies RunBinding);
					if (run.state !== "succeeded") ui.notify(`Remote Pi run ${run.state}: ${run.error ?? "unknown error"}`, "error");
					return;
				}
				pi.appendEntry(RUN_BINDING_ENTRY, { version: 1, runId, workspaceId, state: run.state, lastEvent: cursor } satisfies RunBinding);
				ui.setStatus("scrap-run", ui.theme.fg("accent", `run:${run.state}`));
				if (!useStream) await sleep(400);
			}
		} finally {
			clearInterval(animation);
			ui.setWidget(RUN_WIDGET_KEY, undefined);
			ui.setStatus("scrap-run", stopped ? undefined : ui.theme.fg("accent", "run:detached"));
			if (activeAbort === abort) activeAbort = undefined;
		}
	};

	pi.on("session_start", async (_event, ctx) => {
		stopped = false;
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
		ctx.ui.setStatus("scrap-run", "run:reconnecting");
		void follow(pending.runId, pending.workspaceId, pending.lastEvent, ctx.ui).catch((error) => {
			ctx.ui.setStatus("scrap-run", "run:detached");
			ctx.ui.notify(`Remote run continues, but its event stream is unavailable: ${describeError(error)}`, "warning");
		});
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
		// Inside the worker-side durable runner the agent loop must execute
		// here; intercepting would re-submit the prompt as another run.
		if (process.env.SCRAP_DURABLE_RUN === "1") return;
		if (!session.remoteMode || session.connectedWorkspace === undefined || event.source !== "interactive") return;

		const workspaceId = session.connectedWorkspace.id;
		const sessionKey = ctx.sessionManager.getSessionId();
		try {
			const readiness = await readRunnerReadiness();
			const problem = readinessError(readiness);
			if (problem !== undefined) {
				ctx.ui.setStatus("scrap-run", readiness.durableRuns ? "runner:unauthorized" : "runner:unavailable");
				ctx.ui.notify(`Prompt not started: ${problem}`, "error");
				return { action: "handled" as const };
			}
			// The prompt is accepted only after the active local branch has been
			// durably stored with the run. The worker imports it exactly once, then
			// owns subsequent conversation state independently of this client.
			const contextEntryTypes = new Set([
				"message", "model_change", "thinking_level_change", "compaction",
				"branch_summary", "custom_message", "session_info",
			]);
			let importedParent: string | null = null;
			const importedBranch: Record<string, unknown>[] = [];
			for (const value of ctx.sessionManager.getBranch()) {
				if (typeof value !== "object" || value === null) continue;
				const entry = value as unknown as Record<string, unknown>;
				if (typeof entry.type !== "string" || !contextEntryTypes.has(entry.type) || typeof entry.id !== "string") continue;
				importedBranch.push({ ...entry, parentId: importedParent });
				importedParent = entry.id;
			}
			const sessionSnapshot = [
				{
					type: "scraps_checkpoint",
					version: 1,
					provider: ctx.model?.provider,
					model: ctx.model?.id,
					thinkingLevel: ctx.thinkingLevel,
				},
				...importedBranch,
			];
			const run = await session.requireClient().createRun(workspaceId, event.text, sessionKey, sessionSnapshot);
			pi.sendMessage({ customType: "scraps-remote-user", content: event.text, display: true });
			pi.appendEntry(RUN_BINDING_ENTRY, { version: 1, runId: run.id, workspaceId, state: run.state, lastEvent: 0 } satisfies RunBinding);
			ctx.ui.setStatus("scrap-run", `run:${run.state}`);
			await follow(run.id, workspaceId, 0, ctx.ui);
		} catch (error) {
			ctx.ui.setStatus("scrap-run", "run:detached");
			ctx.ui.notify(`Cannot follow durable Pi run: ${describeError(error)}. If it was accepted, it continues on the worker.`, "warning");
		}
		return { action: "handled" as const };
	});
}
