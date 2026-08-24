/**
 * System-prompt context for remote workspace mode (ADR 0001: "add remote
 * workspace context to the system prompt").
 *
 * The local filesystem shown by the shell is not the filesystem visible to
 * the agent, so the prompt must state clearly where every tool executes and
 * what to do when the workspace is unavailable.
 */

export type RemotePromptContext = {
	readonly workspaceId: string;
	readonly project: string | undefined;
	readonly daemonUrl: string;
	readonly root: string;
	readonly status: string;
};

/** The context block appended to the system prompt in remote mode. */
export function remoteContextBlock(context: RemotePromptContext): string {
	const project = context.project === undefined ? "unknown" : context.project;
	return [
		"## Scraps remote workspace",
		"",
		"You are operating inside a Scraps workspace: an isolated remote Linux",
		"machine that is the authoritative development computer for this session.",
		"",
		`- Workspace: ${context.workspaceId} (${context.status})`,
		`- Project: ${project}`,
		`- Remote project root: ${context.root}`,
		`- Control daemon: ${context.daemonUrl}`,
		"",
		"All project-facing tools (bash, read, write, edit, ls, find, grep) execute",
		"on the remote workspace, not on the local machine. Paths are interpreted",
		"relative to the remote project root. The local filesystem is not accessible.",
		"",
		"If a tool reports that the Scraps workspace is unavailable, stop and tell",
		"the user. Do not attempt to work around it: local fallback does not exist.",
	].join("\n");
}

/**
 * Adapt Pi's system prompt for remote mode: point the working-directory line
 * at the remote root and append the Scraps context block.
 */
export function adaptSystemPrompt(
	systemPrompt: string,
	localCwd: string,
	context: RemotePromptContext,
): string {
	let prompt = systemPrompt;
	const cwdLine = `Current working directory: ${localCwd}`;
	if (prompt.includes(cwdLine)) {
		prompt = prompt.replace(
			cwdLine,
			`Current working directory: ${context.root} (Scraps remote workspace)`,
		);
	}
	return `${prompt}\n\n${remoteContextBlock(context)}`;
}
