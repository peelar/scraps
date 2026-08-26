import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, it } from "node:test";

import {
	approvedCommandEnvironment,
	approvedCommandEnvironmentState,
	clientEnvironment,
} from "./config.ts";

describe("clientEnvironment", () => {
	it("loads a profile while preserving explicit overrides", () => {
		const dir = fs.mkdtempSync(path.join(os.tmpdir(), "scraps-config-"));
		const file = path.join(dir, "client.json");
		fs.writeFileSync(file, JSON.stringify({ daemon_url: "https://worker.example", token: "profile" }));
		assert.deepEqual(clientEnvironment({ SCRAPS_CLIENT_CONFIG: file }), {
			SCRAPS_CLIENT_CONFIG: file,
			SCRAP_DAEMON_URL: "https://worker.example",
			SCRAP_TOKEN: "profile",
		});
		assert.equal(clientEnvironment({ SCRAPS_CLIENT_CONFIG: file, SCRAP_TOKEN: "override" }).SCRAP_TOKEN, "override");
		fs.rmSync(dir, { recursive: true });
	});
});

describe("approvedCommandEnvironment", () => {
	it("copies only approved, set values and never values from the profile", () => {
		const dir = fs.mkdtempSync(path.join(os.tmpdir(), "scraps-config-"));
		const file = path.join(dir, "client.json");
		fs.writeFileSync(file, JSON.stringify({
			env_allow: ["DATABASE_URL", "EMPTY_VALUE", "MISSING", "PATH", "OPENSHELL_TOKEN", "BAD-NAME"],
		}));
		assert.deepEqual(
			approvedCommandEnvironment({
				SCRAPS_CLIENT_CONFIG: file,
				DATABASE_URL: "postgres://sentinel",
				EMPTY_VALUE: "",
				PATH: "/sentinel/path",
				OPENSHELL_TOKEN: "sentinel-openshell",
			}),
			{ DATABASE_URL: "postgres://sentinel", EMPTY_VALUE: "" },
		);
		fs.rmSync(dir, { recursive: true });
	});

	it("uses XDG_CONFIG_HOME when no explicit profile path is set", () => {
		const dir = fs.mkdtempSync(path.join(os.tmpdir(), "scraps-xdg-"));
		const profileDir = path.join(dir, "scraps");
		fs.mkdirSync(profileDir);
		fs.writeFileSync(path.join(profileDir, "client.json"), JSON.stringify({ env_allow: ["DATABASE_URL"] }));
		assert.deepEqual(
			approvedCommandEnvironment({ XDG_CONFIG_HOME: dir, DATABASE_URL: "postgres://sentinel" }),
			{ DATABASE_URL: "postgres://sentinel" },
		);
		fs.rmSync(dir, { recursive: true });
	});

	it("reports loaded and missing approved names without exposing values in diagnostics", () => {
		const dir = fs.mkdtempSync(path.join(os.tmpdir(), "scraps-config-"));
		const file = path.join(dir, "client.json");
		fs.writeFileSync(file, JSON.stringify({ env_allow: ["MISSING", "DATABASE_URL", "DATABASE_URL"] }));
		const state = approvedCommandEnvironmentState({
			SCRAPS_CLIENT_CONFIG: file,
			DATABASE_URL: "postgres://sentinel",
		});
		assert.deepEqual(state.values, { DATABASE_URL: "postgres://sentinel" });
		assert.deepEqual(state.loaded, ["DATABASE_URL"]);
		assert.deepEqual(state.missing, ["MISSING"]);
		assert.ok(!JSON.stringify({ loaded: state.loaded, missing: state.missing }).includes("sentinel"));
		fs.rmSync(dir, { recursive: true });
	});
});
