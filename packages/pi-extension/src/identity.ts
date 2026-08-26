/**
 * Extension identity resolution.
 *
 * ADR 0001: the canonical interactive integration runs Pi locally. `/scrap`
 * activates the seven remote project-facing tools dynamically. The `--scrap`
 * and `--workspace` flags remain compatible startup inputs for internal and
 * test use.
 */

import { DEFAULT_DAEMON_URL } from "./client.ts";

export type RemoteConfig = {
	/** Base URL of the scrapd control daemon (SCRAP_DAEMON_URL). */
	readonly daemonUrl: string | undefined;
	/** Workspace to attach to: `--workspace` flag wins over SCRAP_WORKSPACE_ID. */
	readonly workspaceId: string | undefined;
	/** Project slug such as `owner/repository` (SCRAP_PROJECT). */
	readonly project: string | undefined;
	/** Bearer token for scrapd (SCRAP_TOKEN). */
	readonly token: string | undefined;
};

export type ExtensionMode =
	| { readonly kind: "local" }
	| { readonly kind: "remote"; readonly config: RemoteConfig };

export const SESSION_BINDING_ENTRY = "scraps-workspace-v1";

export type SessionBinding = {
	readonly version: 1;
	readonly mode: "local" | "remote";
	readonly daemonUrl?: string;
	readonly workspaceId?: string;
	readonly project?: string;
};

export type IdentityInputs = {
	/** Raw flag values as reported by `pi.getFlag`. */
	readonly flags: {
		readonly scrap?: unknown;
		readonly workspace?: unknown;
	};
	/** Environment variables (usually `process.env`). */
	readonly env: Record<string, string | undefined>;
};

function asString(value: unknown): string | undefined {
	return typeof value === "string" && value.length > 0 ? value : undefined;
}

/**
 * Decide whether this Pi process runs in remote-workspace mode.
 *
 * Startup remote mode is fail-closed by construction: an unreachable daemon
 * still counts as remote so replaced tools error visibly instead of silently
 * touching the local machine. `/scrap` may activate the same mode later.
 */
export function resolveMode(inputs: IdentityInputs): ExtensionMode {
	if (inputs.flags.scrap !== true) {
		return { kind: "local" };
	}

	return {
		kind: "remote",
		config: {
			// ADR 0002: base URL default http://127.0.0.1:8484; `/scrap`
			// always sets SCRAP_DAEMON_URL explicitly.
			daemonUrl: asString(inputs.env.SCRAP_DAEMON_URL) ?? DEFAULT_DAEMON_URL,
			workspaceId:
				asString(inputs.flags.workspace) ?? asString(inputs.env.SCRAP_WORKSPACE_ID),
			project: asString(inputs.env.SCRAP_PROJECT),
			token: asString(inputs.env.SCRAP_TOKEN),
		},
	};
}

/** Return the latest Scraps binding on the active Pi session branch. */
export function restoreSessionBinding(entries: readonly unknown[]): SessionBinding | undefined {
	let binding: SessionBinding | undefined;
	for (const value of entries) {
		if (typeof value !== "object" || value === null) continue;
		const entry = value as { type?: unknown; customType?: unknown; data?: unknown };
		if (entry.type !== "custom" || entry.customType !== SESSION_BINDING_ENTRY) continue;
		if (typeof entry.data !== "object" || entry.data === null) continue;
		const data = entry.data as Partial<SessionBinding>;
		if (data.version !== 1 || (data.mode !== "local" && data.mode !== "remote")) continue;
		binding = {
			version: 1,
			mode: data.mode,
			...(typeof data.daemonUrl === "string" ? { daemonUrl: data.daemonUrl } : {}),
			...(typeof data.workspaceId === "string" ? { workspaceId: data.workspaceId } : {}),
			...(typeof data.project === "string" ? { project: data.project } : {}),
		};
	}
	return binding;
}

/**
 * Resolve startup identity, allowing an explicit --scrap invocation to win
 * while filling its workspace from the persisted session association.
 */
export function resolveSessionMode(
	inputs: IdentityInputs,
	binding: SessionBinding | undefined,
): ExtensionMode {
	const cli = resolveMode(inputs);
	if (cli.kind === "remote") {
		return {
			kind: "remote",
			config: {
				...cli.config,
				workspaceId: cli.config.workspaceId ?? (binding?.mode === "remote" ? binding.workspaceId : undefined),
				project: cli.config.project ?? (binding?.mode === "remote" ? binding.project : undefined),
			},
		};
	}
	if (binding?.mode !== "remote") return { kind: "local" };
	return {
		kind: "remote",
		config: {
			daemonUrl: binding.daemonUrl ?? asString(inputs.env.SCRAP_DAEMON_URL) ?? DEFAULT_DAEMON_URL,
			workspaceId: binding.workspaceId,
			project: binding.project,
			// Credentials are deliberately re-resolved, never persisted in Pi sessions.
			token: asString(inputs.env.SCRAP_TOKEN),
		},
	};
}
