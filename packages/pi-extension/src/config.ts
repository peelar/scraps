import fs from "node:fs";
import os from "node:os";
import path from "node:path";

type ClientProfile = {
	readonly daemon_url?: unknown;
	readonly token?: unknown;
	readonly env_allow?: unknown;
};

const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
const RESERVED_ENV = new Set([
	"HOME",
	"PATH",
	"SHELL",
	"TMPDIR",
	"SCRAP_WORKSPACE_ROOT",
	"SCRAP_TOKEN",
	"SCRAPS_CLIENT_CONFIG",
]);

function readClientProfile(env: Record<string, string | undefined>): ClientProfile {
	const configDir = env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
	const profilePath = env.SCRAPS_CLIENT_CONFIG ?? path.join(configDir, "scraps", "client.json");
	try {
		return JSON.parse(fs.readFileSync(profilePath, "utf8")) as ClientProfile;
	} catch {
		return {};
	}
}

/** Load the mode-0600 local profile; explicit environment values always win. */
export function clientEnvironment(
	env: Record<string, string | undefined> = process.env,
): Record<string, string | undefined> {
	const resolved = { ...env };
	const profile = readClientProfile(env);
	if (resolved.SCRAP_DAEMON_URL === undefined && typeof profile.daemon_url === "string") {
		resolved.SCRAP_DAEMON_URL = profile.daemon_url;
	}
	if (resolved.SCRAP_TOKEN === undefined && typeof profile.token === "string") {
		resolved.SCRAP_TOKEN = profile.token;
	}
	return resolved;
}

/** Snapshot only locally approved variables that are set when Pi starts. */
export function approvedCommandEnvironment(
	env: Record<string, string | undefined> = process.env,
): Readonly<Record<string, string>> {
	return approvedCommandEnvironmentState(env).values;
}

export type ApprovedCommandEnvironmentState = {
	/** Values copied to sandbox commands. */
	readonly values: Readonly<Record<string, string>>;
	/** Valid, approved names whose values were present when Pi started. */
	readonly loaded: readonly string[];
	/** Valid, approved names that were absent when Pi started. */
	readonly missing: readonly string[];
};

/**
 * Snapshot approved values and retain name-only diagnostics. The diagnostics
 * let Pi and its agent explain the security boundary without ever displaying
 * or persisting a value.
 */
export function approvedCommandEnvironmentState(
	env: Record<string, string | undefined> = process.env,
): ApprovedCommandEnvironmentState {
	const allow = readClientProfile(env).env_allow;
	if (!Array.isArray(allow)) return { values: Object.freeze({}), loaded: [], missing: [] };

	const approved: Record<string, string> = {};
	const missing: string[] = [];
	const names = [...new Set(allow.filter((name): name is string => typeof name === "string"))].sort();
	for (const name of names) {
		if (
			!ENV_NAME.test(name) ||
			RESERVED_ENV.has(name) ||
			name.startsWith("OPENSHELL_")
		) {
			continue;
		}
		const value = env[name];
		if (value === undefined) {
			missing.push(name);
		} else {
			approved[name] = value;
		}
	}
	return {
		values: Object.freeze(approved),
		loaded: Object.freeze(Object.keys(approved)),
		missing: Object.freeze(missing),
	};
}
