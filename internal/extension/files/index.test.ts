import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { describeWorkspace } from "./index.ts";

describe("describeWorkspace", () => {
  it("recognizes a process outside a Scraps workspace", () => {
    assert.equal(
      describeWorkspace({ daemonUrl: undefined, id: undefined, project: undefined }),
      "Pi is not running inside a Scraps workspace.",
    );
  });

  it("includes the workspace context", () => {
    assert.equal(
      describeWorkspace({
        daemonUrl: "http://scrapd.internal:8484",
        id: "quiet-river",
        project: "owner/project",
      }),
      [
        "Workspace: quiet-river",
        "Project: owner/project",
        "Daemon: http://scrapd.internal:8484",
      ].join("\n"),
    );
  });
});
