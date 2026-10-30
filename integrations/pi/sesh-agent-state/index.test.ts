import assert from "node:assert/strict";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createSeshAgentStateExtension } from "./index.ts";

type Handler = (event: any, ctx: any) => unknown;

function mockPi() {
	const handlers = new Map<string, Handler>();
	return {
		pi: {
			on(event: string, handler: Handler) {
				handlers.set(event, handler);
				return () => handlers.delete(event);
			},
		},
		handler(event: string): Handler {
			const handler = handlers.get(event);
			assert.ok(handler, `missing ${event} handler`);
			return handler;
		},
	};
}

function context(sessionID: () => string, idle = true) {
	return {
		hasUI: true,
		isIdle: () => idle,
		sessionManager: { getSessionId: sessionID },
	};
}

function arg(argv: readonly string[], name: string): string {
	const index = argv.indexOf(name);
	assert.notEqual(index, -1, `missing ${name} in ${argv.join(" ")}`);
	return argv[index + 1];
}

async function nextMicrotask(): Promise<void> {
	await Promise.resolve();
	await Promise.resolve();
}

test("session binding captures identity at callback time and dedupes event plus session", async () => {
	const calls: string[][] = [];
	const releases: Array<() => void> = [];
	const extension = createSeshAgentStateExtension({
		threadID: "thread-1",
		runCommand: (argv) => {
			calls.push([...argv]);
			return new Promise<void>((resolve) => releases.push(resolve));
		},
	});
	const runtime = mockPi();
	extension(runtime.pi as never);

	let currentSession = "session-a";
	const ctx = context(() => currentSession);
	const first = Promise.resolve(runtime.handler("session_start")({ type: "session_start", reason: "startup" }, ctx));
	assert.equal(calls.length, 1);
	assert.equal(arg(calls[0], "--event"), "turn_ended");
	assert.equal(arg(calls[0], "--agent-session"), "session-a");

	currentSession = "session-b";
	const replacement = Promise.resolve(runtime.handler("session_start")({ type: "session_start", reason: "new" }, ctx));
	const duplicate = Promise.resolve(runtime.handler("session_start")({ type: "session_start", reason: "reload" }, ctx));
	currentSession = "session-c";
	assert.equal(calls.length, 1, "replacement must serialize behind the in-flight report");

	releases.shift()?.();
	await nextMicrotask();
	assert.equal(calls.length, 2);
	assert.equal(arg(calls[1], "--event"), "turn_ended");
	assert.equal(arg(calls[1], "--agent-session"), "session-b", "queued identity must not read the later session-c value");
	releases.shift()?.();
	await Promise.all([first, replacement, duplicate]);
	assert.equal(calls.length, 2, "the duplicate event/session pair must be deduped");
});

test("all lifecycle reports carry the callback's current session and release stays serialized", async () => {
	const calls: string[][] = [];
	const extension = createSeshAgentStateExtension({
		threadID: "thread-2",
		runCommand: async (argv) => {
			calls.push([...argv]);
		},
	});
	const runtime = mockPi();
	extension(runtime.pi as never);
	const ctx = context(() => "session-live");

	await runtime.handler("session_start")({ type: "session_start", reason: "startup" }, ctx);
	await runtime.handler("agent_start")({ type: "agent_start" }, ctx);
	await runtime.handler("agent_settled")({ type: "agent_settled", aborted: false }, ctx);
	await runtime.handler("agent_settled")({ type: "agent_settled", aborted: false }, ctx);
	await runtime.handler("session_shutdown")({ type: "session_shutdown", reason: "quit" }, ctx);

	assert.deepEqual(calls.map((argv) => arg(argv, "--event")), ["turn_ended", "turn_started", "turn_ended", "release"]);
	assert.deepEqual(calls.map((argv) => arg(argv, "--agent-session")), [
		"session-live",
		"session-live",
		"session-live",
		"session-live",
	]);
});

test("reporter failure remains fail-silent toward native Pi lifecycle", async () => {
	const extension = createSeshAgentStateExtension({
		threadID: "thread-3",
		runCommand: async () => {
			throw new Error("isolated daemon unavailable");
		},
	});
	const runtime = mockPi();
	extension(runtime.pi as never);
	const ctx = context(() => "session-offline");

	await assert.doesNotReject(() => runtime.handler("session_start")({ type: "session_start", reason: "startup" }, ctx));
	await assert.doesNotReject(() => runtime.handler("agent_start")({ type: "agent_start" }, ctx));
	await assert.doesNotReject(() => runtime.handler("agent_settled")({ type: "agent_settled", aborted: false }, ctx));
});

test("production execFile path sends the exact shell-free report-state argv", async () => {
	const dir = await mkdtemp(join(tmpdir(), "sesh-agent-state-"));
	const binary = join(dir, "fake-sesh");
	const log = join(dir, "argv.json");
	const previousLog = process.env.SESH_AGENT_STATE_TEST_LOG;
	try {
		await writeFile(
			binary,
			'#!/usr/bin/env node\nrequire("node:fs").writeFileSync(process.env.SESH_AGENT_STATE_TEST_LOG, JSON.stringify(process.argv.slice(2)));\n',
		);
		await chmod(binary, 0o700);
		process.env.SESH_AGENT_STATE_TEST_LOG = log;
		const extension = createSeshAgentStateExtension({ threadID: "thread-exec", seshBin: binary });
		const runtime = mockPi();
		extension(runtime.pi as never);
		await runtime.handler("session_start")(
			{ type: "session_start", reason: "startup" },
			context(() => "session-exec"),
		);
		const argv = JSON.parse(await readFile(log, "utf8"));
		assert.deepEqual(argv, [
			"thread",
			"report-state",
			"--id",
			"thread-exec",
			"--source",
			"sesh:pi-ext",
			"--event",
			"turn_ended",
			"--agent-session",
			"session-exec",
		]);
	} finally {
		if (previousLog === undefined) delete process.env.SESH_AGENT_STATE_TEST_LOG;
		else process.env.SESH_AGENT_STATE_TEST_LOG = previousLog;
		await rm(dir, { recursive: true, force: true });
	}
});
