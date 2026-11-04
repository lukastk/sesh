import assert from "node:assert/strict";
import test from "node:test";

import { applyThreadEnv, parseThreadEnv, registerSeshThreadEnv } from "./env.ts";

type Handler = (event: any, ctx: any) => unknown;

function mockPi() {
	const handlers = new Map<string, Handler>();
	return {
		pi: { on: (event: string, h: Handler) => void handlers.set(event, h) } as any,
		handlers,
	};
}

test("inert without an env file", () => {
	const m = mockPi();
	registerSeshThreadEnv(m.pi, { envFile: "" });
	assert.equal(m.handlers.size, 0);
});

test("applies additions, changes and removals before each run", async () => {
	const m = mockPi();
	const target: Record<string, string | undefined> = { KEEP: "1", OLD: "x", GONE: "y" };
	let file = JSON.stringify({ vars: { OLD: "new", ADDED: "a" }, removed: ["GONE"] });
	const paths: string[] = [];
	registerSeshThreadEnv(m.pi, {
		envFile: "/home/.sesh/thread-env/t1/env.sh",
		target,
		readFile: (p) => (paths.push(p), file),
	});
	const run = m.handlers.get("before_agent_start")!;
	await run({}, {});
	assert.deepEqual(target, { KEEP: "1", OLD: "new", ADDED: "a" });
	assert.equal(paths[0], "/home/.sesh/thread-env/t1/env.json");

	file = JSON.stringify({ vars: { GONE: "back" }, removed: ["OLD", "ADDED"] });
	await run({}, {});
	assert.deepEqual(target, { KEEP: "1", GONE: "back" });
});

test("a failure is shown once and never breaks the run", async () => {
	const m = mockPi();
	const notes: string[] = [];
	registerSeshThreadEnv(m.pi, {
		envFile: "/x/env.sh",
		target: {},
		readFile: () => {
			throw new Error("boom");
		},
	});
	const run = m.handlers.get("before_agent_start")!;
	const ctx = { ui: { notify: (n: string) => notes.push(n) } };
	await run({}, ctx);
	await run({}, ctx);
	assert.equal(notes.length, 1);
	assert.match(notes[0], /boom/);
});

test("parseThreadEnv tolerates absent fields", () => {
	assert.deepEqual(parseThreadEnv("{}"), { vars: {}, removed: [] });
	const target: Record<string, string | undefined> = {};
	applyThreadEnv(target, parseThreadEnv('{"vars":{"A":"1"}}'));
	assert.deepEqual(target, { A: "1" });
});
