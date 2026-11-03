import assert from "node:assert/strict";
import test from "node:test";

import { CONTEXT_MESSAGE_TYPE, CONTEXT_SECTION, parseContextAnswer, registerSeshThreadContext } from "./context.ts";

type Handler = (event: any, ctx: any) => unknown;

function mockPi() {
	const handlers = new Map<string, Handler>();
	return {
		pi: { on: (event: string, h: Handler) => void handlers.set(event, h) } as any,
		handlers,
	};
}

function ctx(sessionID = "sess-1", notes: string[] = []) {
	return {
		sessionManager: { getSessionId: () => sessionID },
		ui: { notify: (m: string) => notes.push(m) },
	};
}

function event(sections?: Record<string, string>) {
	return { type: "before_agent_start", systemPromptOptions: { sections: sections ?? { other: "kept" } } };
}

test("inert outside a sesh thread", () => {
	const m = mockPi();
	registerSeshThreadContext(m.pi, { threadID: "", runCommand: async () => "{}" });
	assert.equal(m.handlers.size, 0);
});

test("asks with the session id and installs the context as its own section", async () => {
	const m = mockPi();
	const calls: (readonly string[])[] = [];
	registerSeshThreadContext(m.pi, {
		threadID: "t1",
		runCommand: async (argv) => {
			calls.push(argv);
			return JSON.stringify({ system: "BODY", message: "" });
		},
	});
	const ev = event();
	const result = await m.handlers.get("before_agent_start")!(ev, ctx("sess-9"));
	assert.deepEqual(calls, [["context", "hook", "--agent", "pi", "--session", "sess-9"]]);
	assert.equal(ev.systemPromptOptions.sections[CONTEXT_SECTION], "BODY");
	assert.equal(ev.systemPromptOptions.sections.other, "kept", "other sections untouched");
	assert.equal(result, undefined, "no notice without a change");
});

test("no context leaves the prompt alone", async () => {
	const m = mockPi();
	registerSeshThreadContext(m.pi, { threadID: "t1", runCommand: async () => JSON.stringify({ system: "", message: "" }) });
	const ev = event();
	await m.handlers.get("before_agent_start")!(ev, ctx());
	assert.equal(CONTEXT_SECTION in ev.systemPromptOptions.sections, false);
});

test("a change notice becomes a hidden custom message in the run", async () => {
	const m = mockPi();
	registerSeshThreadContext(m.pi, { threadID: "t1", runCommand: async () => JSON.stringify({ system: "", message: "REMOVED" }) });
	const result: any = await m.handlers.get("before_agent_start")!(event(), ctx());
	assert.deepEqual(result, { message: { customType: CONTEXT_MESSAGE_TYPE, content: "REMOVED", display: false } });
});

test("a failure warns once and never throws", async () => {
	const m = mockPi();
	const notes: string[] = [];
	registerSeshThreadContext(m.pi, { threadID: "t1", runCommand: async () => { throw new Error("daemon down"); } });
	const h = m.handlers.get("before_agent_start")!;
	const ev = event();
	assert.equal(await h(ev, ctx("s", notes)), undefined);
	await h(ev, ctx("s", notes));
	assert.deepEqual(notes, ["sesh thread context unavailable: daemon down"]);
	assert.equal(CONTEXT_SECTION in ev.systemPromptOptions.sections, false);
});

test("a sesh that predates thread context is silent", async () => {
	const m = mockPi();
	const notes: string[] = [];
	registerSeshThreadContext(m.pi, {
		threadID: "t1",
		runCommand: async () => { throw new Error('sesh: unknown command "context"\nsesh — multi-machine…'); },
	});
	await m.handlers.get("before_agent_start")!(event(), ctx("s", notes));
	assert.deepEqual(notes, []);
});

test("parseContextAnswer tolerates empty output and ignores junk fields", () => {
	assert.deepEqual(parseContextAnswer(""), { system: "", message: "" });
	assert.deepEqual(parseContextAnswer('{"system":1,"message":"m","x":2}'), { system: "", message: "m" });
	assert.throws(() => parseContextAnswer("not json"));
});
