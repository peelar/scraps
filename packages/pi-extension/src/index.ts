import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type WorkspaceContext = {
  daemonUrl: string | undefined;
  id: string | undefined;
  project: string | undefined;
};

function readWorkspaceContext(): WorkspaceContext {
  return {
    daemonUrl: process.env.SCRAP_DAEMON_URL,
    id: process.env.SCRAP_WORKSPACE_ID,
    project: process.env.SCRAP_PROJECT,
  };
}

export function describeWorkspace(workspace: WorkspaceContext): string {
  if (!workspace.id) {
    return "Pi is not running inside a Scraps workspace.";
  }

  return [
    `Workspace: ${workspace.id}`,
    `Project: ${workspace.project ?? "unknown"}`,
    `Daemon: ${workspace.daemonUrl ?? "not configured"}`,
  ].join("\n");
}

export default function scrapsExtension(pi: ExtensionAPI): void {
  const workspace = readWorkspaceContext();

  pi.on("session_start", async (_event, context) => {
    if (workspace.id) {
      context.ui.setStatus("scraps", `scrap:${workspace.id}`);
    }
  });

  pi.registerCommand("scrap", {
    description: "Show the current Scraps workspace",
    handler: async (_args, context) => {
      context.ui.notify(describeWorkspace(workspace), "info");
    },
  });
}
