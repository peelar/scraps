import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import type { RunEvent, RunRecord } from "./client.ts";
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

function terminal(state: RunRecord["state"]): boolean {
	return state === "succeeded" || state === "failed" || state === "cancelled";
}

function textFromEvent(event: RunEvent): string | undefined {
	if (event.data.type !== "message_end") return undefined;
	const message = event.data.message;
	if (typeof message !== "object" || message === null || (message as { role?: unknown }).role !== "assistant") return undefined;
	const content = (message as { content?: unknown }).content;
	if (!Array.isArray(content)) return undefined;
	const text = content
		.map((part) => typeof part === "object" && part !== null && (part as { type?: unknown }).type === "text" ? (part as { text?: unknown }).text : undefined)
		.filter((part): part is string => typeof part === "string")
		.join("");
	return text.length > 0 ? text : undefined;
}

/**
 * Route every interactive Scraps prompt to scrapd's durable Pi runner.
 * Durable execution is the `/scrap` contract; unsupported workers fail closed
 * instead of silently returning ownership of the agent loop to the laptop.
 */
export function registerDurableRuns(pi: ExtensionAPI, session: WorkspaceSession): void {
	let runnerReadiness: { durableRuns: boolean; modelAuth: boolean } | undefined;
	let stopped = false;

	const readRunnerReadiness = async (): Promise<{ durableRuns: boolean; modelAuth: boolean }> => {
		if (runnerReadiness?.durableRuns === true && runnerReadiness.modelAuth === true) return runnerReadiness;
		const features = (await session.requireClient().info()).features;
		runnerReadiness = { durableRuns: features?.durableRuns === true, modelAuth: features?.modelAuth === true };
		return runnerReadiness;
	};

	const readinessError = (readiness: { durableRuns: boolean; modelAuth: boolean }): string | undefined => {
		if (!readiness.durableRuns) return "This Scraps worker does not provide the required durable Pi runner. Upgrade or configure the worker before submitting a prompt.";
		if (!readiness.modelAuth) return "The durable Pi runner has no model authorization. On the worker, run: sudo scraps-worker model-auth anthropic";
		return undefined;
	};

	const showEvent = (event: RunEvent): void => {
		const text = textFromEvent(event);
		if (text !== undefined) {
			pi.sendMessage({ customType: "scraps-remote-assistant", content: text, display: true });
		}
	};

	const follow = async (
		runId: string,
		workspaceId: string,
		after: number,
		ui: { setStatus: (key: string, text: string | undefined) => void; notify: (message: string, level: "info" | "warning" | "error") => void },
	): Promise<void> => {
		let cursor = after;
		while (!stopped) {
			const events = await session.requireClient().runEvents(runId, cursor);
			for (const event of events) {
				cursor = event.sequence;
				showEvent(event);
			}
			const run = await session.requireClient().getRun(runId);
			if (terminal(run.state)) {
				// The terminal state is persisted after the executor's final event,
				// but that event may have landed between our first event query and
				// this state query. Close that race before saving the cursor.
				const finalEvents = await session.requireClient().runEvents(runId, cursor);
				for (const event of finalEvents) {
					cursor = event.sequence;
					showEvent(event);
				}
			}
			pi.appendEntry(RUN_BINDING_ENTRY, { version: 1, runId, workspaceId, state: run.state, lastEvent: cursor } satisfies RunBinding);
			if (terminal(run.state)) {
				ui.setStatus("scrap-run", undefined);
				if (run.state !== "succeeded") ui.notify(`Remote Pi run ${run.state}: ${run.error ?? "unknown error"}`, "error");
				return;
			}
			ui.setStatus("scrap-run", `run:${run.state}`);
			await new Promise((resolve) => setTimeout(resolve, 500));
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

	pi.on("session_shutdown", () => { stopped = true; });

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
