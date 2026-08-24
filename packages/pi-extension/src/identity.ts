/**
 * Extension identity resolution.
 *
 * ADR 0001: the canonical interactive integration runs Pi locally and replaces
 * the seven project-facing tools only behind an explicit `--scrap` flag. The
 * `scrap pi` convenience launcher is expected to start Pi with `--scrap` (and
 * usually `--workspace <id>`) while injecting the remaining identity through
 * `SCRAP_*` environment variables.
 */

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
 * Remote mode is fail-closed by construction: it is only entered through the
 * explicit `--scrap` flag, and an incomplete configuration (for example a
 * missing daemon URL) still counts as remote so that the replaced tools error
 * visibly instead of silently touching the local machine.
 */
export function resolveMode(inputs: IdentityInputs): ExtensionMode {
	if (inputs.flags.scrap !== true) {
		return { kind: "local" };
	}

	return {
		kind: "remote",
		config: {
			daemonUrl: asString(inputs.env.SCRAP_DAEMON_URL),
			workspaceId:
				asString(inputs.flags.workspace) ?? asString(inputs.env.SCRAP_WORKSPACE_ID),
			project: asString(inputs.env.SCRAP_PROJECT),
			token: asString(inputs.env.SCRAP_TOKEN),
		},
	};
}
