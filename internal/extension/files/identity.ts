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
