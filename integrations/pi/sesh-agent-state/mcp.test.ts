import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { MCP_ARGS_FLAG, McpConnection, agentDir, expandHome, parseMcpArgs, readGlobalEntry, registerSeshMcp, toToolResult } from "./mcp.ts";

// A real stdio server, as a child process: it records its argv and env, offers two
// tools over two pages, and answers `boom` as a tool error and `die` by exiting.
const SERVER = `
const fs = require("node:fs");
fs.writeFileSync(process.argv[2], JSON.stringify({ argv: process.argv.slice(3), env: process.env.FROM_ENTRY ?? null, cwd: process.cwd() }));
let buffer = "";
const reply = (id, result) => process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\\n");
process.stdin.on("data", (chunk) => {
	buffer += chunk;
	let end;
	while ((end = buffer.indexOf("\\n")) !== -1) {
		const message = JSON.parse(buffer.slice(0, end));
		buffer = buffer.slice(end + 1);
		if (message.method === "initialize") reply(message.id, { protocolVersion: message.params.protocolVersion, capabilities: {}, serverInfo: { name: "t", version: "1" } });
		else if (message.method === "tools/list" && !message.params.cursor) reply(message.id, { tools: [{ name: "echo", description: "d", inputSchema: { type: "object", properties: { v: { type: "string" } } } }], nextCursor: "2" });
		else if (message.method === "tools/list") reply(message.id, { tools: [{ name: "boom" }, { name: "die" }] });
		else if (message.method === "tools/call" && message.params.name === "echo") reply(message.id, { content: [{ type: "text", text: message.params.arguments.v }] });
		else if (message.method === "tools/call" && message.params.name === "boom") reply(message.id, { isError: true, content: [{ type: "text", text: "it broke" }] });
		else if (message.method === "tools/call" && message.params.name === "die") process.exit(3);
	}
});
`;

function fixture(entry: Record<string, unknown> = {}) {
	const dir = mkdtempSync(join(tmpdir(), "sesh-mcp-"));
	const server = join(dir, "server.cjs");
	const log = join(dir, "log.json");
	writeFileSync(server, SERVER);
	writeFileSync(
		join(dir, "mcp.json"),
		JSON.stringify({ mcpServers: { soliton: { command: process.execPath, args: [server, log, "--global"], ...entry } } }),
	);
	return { dir, log };
}

function mockPi(flag?: string) {
	const handlers = new Map<string, (event: any, ctx: any) => unknown>();
	const tools = new Map<string, any>();
	const flags: string[] = [];
	const pi = {
		on: (event: string, handler: any) => void handlers.set(event, handler),
		registerFlag: (name: string) => void flags.push(name),
		getFlag: () => flag,
		registerTool: (tool: any) => void tools.set(tool.name, tool),
	} as any;
	return { pi, handlers, tools, flags };
}

const headless = { hasUI: false };

test("parseMcpArgs accepts a server map and refuses anything else", () => {
	assert.deepEqual(parseMcpArgs('{"a":["1","2"]}'), { a: ["1", "2"] });
	assert.throws(() => parseMcpArgs("nope"), /not JSON/);
	assert.throws(() => parseMcpArgs("[]"), /expected an object/);
	assert.throws(() => parseMcpArgs('{"a":"1"}'), /not a list/);
	assert.throws(() => parseMcpArgs('{"a":[1]}'), /not a list/);
});

test("agentDir honours the launcher's agent dir, mypi's first", () => {
	assert.equal(agentDir({ PI_CODING_AGENT_DIR: "/x" }), "/x");
	assert.equal(agentDir({ MYPI_CODING_AGENT_DIR: "/m", PI_CODING_AGENT_DIR: "/x" }), "/m");
	assert.ok(agentDir({}).endsWith(join(".pi", "agent")));
});

test("readGlobalEntry is loud about a missing server and a URL server", () => {
	const { dir } = fixture();
	assert.throws(() => readGlobalEntry(dir, "absent"), /no global MCP server "absent"/);
	writeFileSync(join(dir, "mcp.json"), JSON.stringify({ mcpServers: { u: { url: "https://x" } } }));
	assert.throws(() => readGlobalEntry(dir, "u"), /only a stdio server/);
});

test("expandHome expands only a leading ~", () => {
	assert.ok(!expandHome("~/x").startsWith("~"));
	assert.equal(expandHome("/a/~/b"), "/a/~/b");
	assert.equal(expandHome("~user/x"), "~user/x");
});

test("toToolResult maps content and turns isError into a failure", () => {
	assert.deepEqual(toToolResult({ content: [{ type: "text", text: "a" }, { type: "image", data: "d", mimeType: "image/gif" }] }).content, [
		{ type: "text", text: "a" },
		{ type: "image", data: "d", mimeType: "image/gif" },
	]);
	assert.throws(() => toToolResult({ isError: true, content: [{ type: "text", text: "bad" }] }), /bad/);
});

test("the flag registers everywhere, and without it nothing starts", async () => {
	const m = mockPi();
	registerSeshMcp(m.pi);
	assert.deepEqual(m.flags, [MCP_ARGS_FLAG]);
	await m.handlers.get("session_start")!({}, headless);
	assert.equal(m.tools.size, 0);
});

test("starts the server with the global args, then the thread's, and calls its tools", async () => {
	const { dir, log } = fixture({ env: { FROM_ENTRY: "yes" } });
	const hostile = "--agent-file=/a b/it's $(x)";
	const m = mockPi(JSON.stringify({ soliton: [hostile, "--two"] }));
	registerSeshMcp(m.pi, { dir });
	await m.handlers.get("session_start")!({}, headless);
	const started = JSON.parse(readFileSync(log, "utf8"));
	assert.deepEqual(started.argv, ["--global", hostile, "--two"]);
	assert.equal(started.env, "yes");
	assert.deepEqual([...m.tools.keys()], ["mcp__soliton__echo", "mcp__soliton__boom", "mcp__soliton__die"]);
	const result = await m.tools.get("mcp__soliton__echo").execute("id", { v: "hello" });
	assert.deepEqual(result.content, [{ type: "text", text: "hello" }]);
	await assert.rejects(m.tools.get("mcp__soliton__boom").execute("id", {}), /it broke/);
	await m.handlers.get("session_shutdown")!({}, headless);
});

test("a server that dies is started again by the next call, without duplicate tools", async () => {
	const { dir } = fixture();
	const m = mockPi('{"soliton":["x"]}');
	registerSeshMcp(m.pi, { dir });
	await m.handlers.get("session_start")!({}, headless);
	await assert.rejects(m.tools.get("mcp__soliton__die").execute("id", {}), /exited \(3\)/);
	const result = await m.tools.get("mcp__soliton__echo").execute("id", { v: "back" });
	assert.deepEqual(result.content, [{ type: "text", text: "back" }]);
	assert.equal(m.tools.size, 3);
	await m.handlers.get("session_shutdown")!({}, headless);
});

test("a repeated session_start keeps the running server", async () => {
	const { dir, log } = fixture();
	const m = mockPi('{"soliton":["x"]}');
	registerSeshMcp(m.pi, { dir });
	await m.handlers.get("session_start")!({}, headless);
	writeFileSync(log, "untouched");
	await m.handlers.get("session_start")!({}, headless);
	assert.equal(readFileSync(log, "utf8"), "untouched");
	await m.handlers.get("session_shutdown")!({}, headless);
});

test("failure exits a headless run and notifies a headed one", async () => {
	const { dir } = fixture();
	const exits: string[] = [];
	const m = mockPi('{"absent":["x"]}');
	registerSeshMcp(m.pi, { dir, exit: (message) => void exits.push(message) });
	await m.handlers.get("session_start")!({}, headless);
	assert.match(exits[0], /no global MCP server "absent"/);

	const notes: string[] = [];
	await m.handlers.get("session_start")!({}, { hasUI: true, ui: { notify: (text: string) => notes.push(text) } });
	assert.equal(exits.length, 1, "a headed pi does not exit");
	assert.match(notes[0], /no global MCP server/);
});

test("a server that cannot start fails with its stderr", async () => {
	const dir = mkdtempSync(join(tmpdir(), "sesh-mcp-"));
	writeFileSync(
		join(dir, "mcp.json"),
		JSON.stringify({ mcpServers: { s: { command: process.execPath, args: ["-e", "console.error('needs a key'); process.exit(2)"] } } }),
	);
	const exits: string[] = [];
	const m = mockPi('{"s":["x"]}');
	registerSeshMcp(m.pi, { dir, exit: (message) => void exits.push(message) });
	await m.handlers.get("session_start")!({}, headless);
	assert.match(exits[0], /needs a key/);
});

test("a command that does not exist fails rather than hangs", async () => {
	const connection = new McpConnection("/nonexistent/server", [], {});
	await assert.rejects(connection.start(), /could not start/);
});

test("a server that says nothing times out", async () => {
	const connection = new McpConnection(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { timeoutMs: 100 });
	await assert.rejects(connection.start(), /no answer/);
	connection.stop();
});

test("a server writing garbage to stdout fails rather than hangs", async () => {
	const connection = new McpConnection(process.execPath, ["-e", "console.log('not json'); setInterval(() => {}, 1000)"], {});
	await assert.rejects(connection.start(), /not JSON-RPC/);
});
