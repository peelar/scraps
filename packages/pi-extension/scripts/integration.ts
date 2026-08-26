/**
 * LLM-free integration harness for the extension ↔ scrapd surface.
 *
 * Drives the extension's real session and operations layers (the same code
 * Pi's replaced tools use) against a live daemon, so contract drift between
 * client.ts and the Go implementation fails loudly without a model in the
 * loop. Complements src/client.test.ts (in-process fake daemon) by testing
 * the actual daemon end to end.
 *
 * Usage:
 *   node --experimental-strip-types packages/pi-extension/scripts/integration.ts \
 *     http://127.0.0.1:8484 [workspace-id]
 *
 * Creates a throwaway workspace when no id is given and always deletes what
 * it created. Exits non-zero on the first failure.
 */

import { ScrapdClient } from "../src/client.ts";
import { WorkspaceSession } from "../src/workspace.ts";
import {
	createRemoteBashOps,
	createRemoteEditOps,
	createRemoteFindOps,
	createRemoteLsOps,
	createRemoteReadOps,
	runRemoteGrep,
} from "../src/operations.ts";

const [url, existingId] = process.argv.slice(2);
if (url === undefined) {
	console.error("usage: integration.ts <scrapd-url> [workspace-id]");
	process.exit(2);
}

let failures = 0;
function step(name: string, fn: () => Promise<string>) {
	return fn()
		.then((detail) => console.log(`ok   ${name} — ${detail}`))
		.catch((error) => {
			failures += 1;
			console.error(`FAIL ${name} — ${error instanceof Error ? error.message : error}`);
		});
}

const session = new WorkspaceSession((baseUrl, token) => new ScrapdClient(baseUrl, token));
session.configure({
	daemonUrl: url,
	workspaceId: existingId,
	project: "scraps/integration",
	token: process.env.SCRAP_TOKEN,
});

await step("daemon reachable", async () => {
	const info = await session.requireClient().info();
	return `${info.name} ${info.version}`;
});

const created = existingId === undefined;
if (created) {
	await step("create workspace", async () => {
		const workspace = await session.create("scraps/integration");
		return workspace.id;
	});
} else {
	await step("attach workspace", async () => {
		const workspace = await session.connect(existingId);
		return `${workspace.id} (${workspace.state})`;
	});
}

const root = session.root;

await step("exec streams output and exit status", async () => {
	let output = "";
	const result = await session.requireClient().exec(session.workspaceId ?? "", "printf hi && exit 7", root, {
		onData: (chunk) => {
			output += chunk.toString("utf8");
		},
	});
	if (output !== "hi") {
		throw new Error(`unexpected output: ${JSON.stringify(output)}`);
	}
	if (result.exitCode !== 7) {
		throw new Error(`unexpected exit code: ${result.exitCode}`);
	}
	return `output=${JSON.stringify(output)} exit=${result.exitCode}`;
});

await step("bash operations run remotely", async () => {
	const ops = createRemoteBashOps(session);
	let output = "";
	const result = await ops.exec(`pwd > /tmp/scrapd-pwd && cat /tmp/scrapd-pwd`, root, {
		onData: (chunk) => {
			output += chunk.toString("utf8");
		},
	});
	if (result.exitCode !== 0 || !output.trim().startsWith(root)) {
		throw new Error(`pwd returned ${JSON.stringify(output)} (exit ${result.exitCode})`);
	}
	return `pwd=${output.trim()}`;
});

await step("write/read round-trip (binary-safe)", async () => {
	const write = createRemoteEditOps(session);
	const read = createRemoteReadOps(session);
	await write.writeFile(`${root}/integration.bin`, "\u00ff-bin\u00ff");
	const buffer = await read.readFile(`${root}/integration.bin`);
	const text = buffer.toString("utf8");
	if (text !== "\u00ff-bin\u00ff") {
		throw new Error(`round-trip corrupted: ${JSON.stringify(text)}`);
	}
	return `${buffer.byteLength} bytes`;
});

await step("edit preserves exact-match semantics", async () => {
	const ops = createRemoteEditOps(session);
	await ops.writeFile(`${root}/integration.txt`, "alpha\nbeta\n");
	const before = (await ops.readFile(`${root}/integration.txt`)).toString("utf8");
	const after = before.replace("beta", "gamma");
	await ops.writeFile(`${root}/integration.txt`, after);
	const final = (await ops.readFile(`${root}/integration.txt`)).toString("utf8");
	if (final !== "alpha\ngamma\n") {
		throw new Error(`unexpected content: ${JSON.stringify(final)}`);
	}
	return "alpha/gamma swap applied";
});

await step("ls lists directory entries", async () => {
	const ops = createRemoteLsOps(session);
	const entries = await ops.readdir(root);
	for (const expected of ["integration.bin", "integration.txt"]) {
		if (!entries.includes(expected)) {
			throw new Error(`missing ${expected} in ${JSON.stringify(entries)}`);
		}
	}
	return entries.join(", ");
});

await step("find walks workspace paths", async () => {
	const ops = createRemoteFindOps(session);
	const paths = await ops.glob("integration.*", root, { ignore: [], limit: 100 });
	for (const expected of [`${root}/integration.bin`, `${root}/integration.txt`]) {
		if (!paths.includes(expected)) {
			throw new Error(`missing ${expected} in ${JSON.stringify(paths)}`);
		}
	}
	return paths.join(", ");
});

await step("grep formats matches like the built-in", async () => {
	const result = await runRemoteGrep(session, { pattern: "gamma" });
	if (!result.text.includes("integration.txt:2: gamma")) {
		throw new Error(`unexpected grep output: ${result.text}`);
	}
	return result.text.trim();
});

await step("aborting exec kills its remote process group", async () => {
	const client = session.requireClient();
	const controller = new AbortController();
	const running = client.exec(session.workspaceId ?? "", "echo $$ > integration.pid; sleep 600", root, {
		onData: () => {},
		signal: controller.signal,
	});
	await new Promise((resolve) => setTimeout(resolve, 500));
	controller.abort();
	await running.catch(() => {});
	let output = "";
	const probe = await client.exec(
		session.workspaceId ?? "",
		"pid=$(cat integration.pid); if kill -0 \"$pid\" 2>/dev/null; then echo alive; exit 1; fi; echo reaped",
		root,
		{ onData: (chunk) => { output += chunk.toString("utf8"); } },
	);
	if (probe.exitCode !== 0 || output.trim() !== "reaped") {
		throw new Error(`remote process survived abort: ${JSON.stringify(output)}`);
	}
	return output.trim();
});

await step("stop/start preserves files and rejects execution while stopped", async () => {
	const client = session.requireClient();
	const id = session.workspaceId ?? "";
	await client.stopWorkspace(id);
	const stopped = await client.getWorkspace(id);
	if (stopped.state !== "stopped") throw new Error(`unexpected stopped state: ${stopped.state}`);
	let rejected = false;
	try {
		await client.exec(id, "true", root, { onData: () => {} });
	} catch {
		rejected = true;
	}
	if (!rejected) throw new Error("execution succeeded while workspace was stopped");
	await client.startWorkspace(id);
	const content = await client.readFile(id, `${root}/integration.txt`);
	if (content.toString("utf8") !== "alpha\ngamma\n") throw new Error("workspace content was not preserved");
	return "stopped, fail-closed, restarted, data preserved";
});

if (created) {
	await step("delete workspace", async () => {
		const id = session.workspaceId ?? "";
		await session.remove();
		return `${id} removed`;
	});
}

if (failures > 0) {
	console.error(`\n${failures} step(s) failed`);
	process.exit(1);
}
console.log("\nall steps passed");
