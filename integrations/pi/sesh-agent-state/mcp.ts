/**
 * Per-thread MCP arguments for pi and mypi (sesh schema 63). sesh launches a thread
 * that has any with one flag:
 *
 *   --sesh-mcp-args={"soliton":["--agent-file=/path/to/agent.json"]}
 *
 * For each named server, this extension reads the global entry from `mcp.json` in
 * pi's agent dir, starts the server itself with the entry's arguments followed by
 * the thread's, and registers its tools as `mcp__<server>__<tool>`. The server is a
 * child of this pi process and ends with it, so a resume or a headless turn starts
 * it again with the arguments of that launch.
 *
 * It holds its own small stdio client rather than handing the server to the MCP
 * support pi already has: that support reads `mcp.json` as it stands and has no way
 * to add arguments to an entry for one process.
 *
 * Failure is loud. A headless turn exits non-zero before the model runs, because a
 * worker without its tools would otherwise answer without them. A headed pi shows
 * the error, and a tool call retries the start.
 */

import { type ChildProcess, spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

export const MCP_ARGS_FLAG = "sesh-mcp-args";
const PROTOCOL_VERSION = "2025-06-18";
const REQUEST_TIMEOUT_MS = 30_000;
const STDERR_TAIL_BYTES = 2000;

export interface McpEntry {
	command: string;
	args?: string[];
	env?: Record<string, string>;
	cwd?: string;
}

export interface McpTool {
	name: string;
	description?: string;
	inputSchema?: Record<string, unknown>;
}

interface ToolContent {
	type: string;
	text?: string;
	data?: string;
	mimeType?: string;
}

/** Parses the flag's value. Anything but a server → arguments object is an error. */
export function parseMcpArgs(value: string): Record<string, string[]> {
	let parsed: unknown;
	try {
		parsed = JSON.parse(value);
	} catch (err) {
		throw new Error(`--${MCP_ARGS_FLAG}: not JSON: ${(err as Error).message}`);
	}
	if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
		throw new Error(`--${MCP_ARGS_FLAG}: expected an object of server name to argument list`);
	}
	for (const [server, args] of Object.entries(parsed)) {
		if (!Array.isArray(args) || args.some((arg) => typeof arg !== "string")) {
			throw new Error(`--${MCP_ARGS_FLAG}: the arguments of ${server} are not a list of strings`);
		}
	}
	return parsed as Record<string, string[]>;
}

export function agentDir(env: NodeJS.ProcessEnv = process.env): string {
	return env.MYPI_CODING_AGENT_DIR || env.PI_CODING_AGENT_DIR || join(homedir(), ".pi", "agent");
}

/** The global entry of `server` in `<dir>/mcp.json`. */
export function readGlobalEntry(dir: string, server: string): McpEntry {
	const path = join(dir, "mcp.json");
	const config = JSON.parse(readFileSync(path, "utf8"));
	const entry = config?.mcpServers?.[server];
	if (!entry) throw new Error(`no global MCP server "${server}" in ${path}`);
	if (typeof entry.command !== "string" || entry.command === "") {
		throw new Error(`the global MCP server "${server}" in ${path} has no command: only a stdio server takes arguments`);
	}
	return entry;
}

export function expandHome(path: string): string {
	return path === "~" || path.startsWith("~/") ? join(homedir(), path.slice(1)) : path;
}

/** One running server: newline-delimited JSON-RPC over its stdin and stdout. */
export class McpConnection {
	private child: ChildProcess | undefined;
	private buffer = "";
	private stderrTail = "";
	private nextId = 1;
	private pending = new Map<number, { resolve: (value: any) => void; reject: (err: Error) => void; timer: NodeJS.Timeout }>();
	private exitError: Error | undefined;

	private readonly command: string;
	private readonly args: readonly string[];
	private readonly options: { cwd?: string; env?: Record<string, string>; timeoutMs?: number };

	constructor(command: string, args: readonly string[], options: { cwd?: string; env?: Record<string, string>; timeoutMs?: number }) {
		this.command = command;
		this.args = args;
		this.options = options;
	}

	get alive(): boolean {
		return this.child !== undefined && this.exitError === undefined;
	}

	/** Starts the server and returns its tools. */
	async start(): Promise<McpTool[]> {
		const child = spawn(this.command, [...this.args], {
			cwd: this.options.cwd,
			env: { ...process.env, ...this.options.env },
			stdio: ["pipe", "pipe", "pipe"],
		});
		this.child = child;
		child.stdout?.setEncoding("utf8");
		child.stdout?.on("data", (chunk: string) => this.onData(chunk));
		child.stderr?.setEncoding("utf8");
		child.stderr?.on("data", (chunk: string) => {
			this.stderrTail = (this.stderrTail + chunk).slice(-STDERR_TAIL_BYTES);
		});
		// A write to a server that just died must surface through `exit`, not as an
		// unhandled stream error that takes pi down.
		child.stdin?.on("error", () => undefined);
		// spawn reports a missing cwd as ENOENT on the command, so name both.
		child.on("error", (err) => this.fail(new Error(`could not start ${this.command} in ${this.options.cwd ?? process.cwd()}: ${err.message}`)));
		child.on("exit", (code, signal) => {
			const stderr = this.stderrTail.trim();
			this.fail(new Error(`exited (${signal ?? code})${stderr ? `: ${stderr}` : ""}`));
		});
		await this.request("initialize", {
			protocolVersion: PROTOCOL_VERSION,
			capabilities: {},
			clientInfo: { name: "sesh-agent-state", version: "1" },
		});
		this.send({ jsonrpc: "2.0", method: "notifications/initialized" });
		const tools: McpTool[] = [];
		let cursor: string | undefined;
		do {
			const page = await this.request("tools/list", cursor ? { cursor } : {});
			tools.push(...(page.tools ?? []));
			cursor = page.nextCursor;
		} while (cursor);
		return tools;
	}

	async call(tool: string, args: unknown, signal?: AbortSignal): Promise<{ content: ToolContent[]; isError?: boolean }> {
		return this.request("tools/call", { name: tool, arguments: args ?? {} }, signal);
	}

	stop(): void {
		this.child?.kill();
		this.fail(new Error("stopped"));
	}

	private send(message: unknown): void {
		this.child?.stdin?.write(`${JSON.stringify(message)}\n`);
	}

	private request(method: string, params: unknown, signal?: AbortSignal): Promise<any> {
		if (this.exitError) return Promise.reject(this.exitError);
		const id = this.nextId++;
		return new Promise((resolve, reject) => {
			const settle = (run: () => void) => {
				clearTimeout(timer);
				signal?.removeEventListener("abort", onAbort);
				this.pending.delete(id);
				run();
			};
			const onAbort = () => settle(() => reject(new Error("aborted")));
			const timer = setTimeout(
				() => settle(() => reject(new Error(`${method}: no answer in ${this.options.timeoutMs ?? REQUEST_TIMEOUT_MS}ms`))),
				this.options.timeoutMs ?? REQUEST_TIMEOUT_MS,
			);
			signal?.addEventListener("abort", onAbort, { once: true });
			this.pending.set(id, {
				resolve: (value) => settle(() => resolve(value)),
				reject: (err) => settle(() => reject(err)),
				timer,
			});
			this.send({ jsonrpc: "2.0", id, method, params });
		});
	}

	private onData(chunk: string): void {
		this.buffer += chunk;
		for (let end = this.buffer.indexOf("\n"); end !== -1; end = this.buffer.indexOf("\n")) {
			const line = this.buffer.slice(0, end).trim();
			this.buffer = this.buffer.slice(end + 1);
			if (line) this.onMessage(line);
		}
	}

	private onMessage(line: string): void {
		let message: any;
		try {
			message = JSON.parse(line);
		} catch {
			this.child?.kill();
			this.fail(new Error(`wrote something that is not JSON-RPC to stdout: ${line.slice(0, 200)}`));
			return;
		}
		if (typeof message.id === "number" && !message.method) {
			const pending = this.pending.get(message.id);
			if (!pending) return;
			if (message.error) pending.reject(new Error(message.error.message ?? JSON.stringify(message.error)));
			else pending.resolve(message.result ?? {});
		} else if (message.id !== undefined && message.method) {
			// A request from the server. This client offers no capabilities.
			this.send({ jsonrpc: "2.0", id: message.id, error: { code: -32601, message: "method not found" } });
		}
	}

	private fail(err: Error): void {
		this.exitError ??= err;
		for (const pending of [...this.pending.values()]) pending.reject(this.exitError);
	}
}

export function toToolResult(result: { content?: ToolContent[]; isError?: boolean }) {
	const content = (result.content ?? []).map((item) =>
		item.type === "image" && item.data
			? { type: "image" as const, data: item.data, mimeType: item.mimeType ?? "image/png" }
			: { type: "text" as const, text: item.text ?? JSON.stringify(item) },
	);
	if (result.isError) {
		throw new Error(content.map((item) => (item.type === "text" ? item.text : "[image]")).join("\n") || "the tool failed");
	}
	return { content, details: {} };
}

export interface SeshMcpDependencies {
	dir?: string;
	/** Test seam for the failure path of a headless run. */
	exit?: (message: string) => void;
}

function exitLoudly(message: string): void {
	process.stderr.write(`sesh: ${message}\n`);
	process.exit(1);
}

export function registerSeshMcp(pi: ExtensionAPI, dependencies: SeshMcpDependencies = {}): void {
	pi.registerFlag(MCP_ARGS_FLAG, {
		description: "JSON object of MCP server name to the arguments this thread adds to that server's global entry (set by sesh)",
		type: "string",
	});

	const connections = new Map<string, McpConnection>();
	const registered = new Set<string>();

	async function connect(server: string, threadArgs: string[]): Promise<void> {
		const entry = readGlobalEntry(dependencies.dir ?? agentDir(), server);
		const connection = new McpConnection(entry.command, [...(entry.args ?? []), ...threadArgs], {
			cwd: entry.cwd ? expandHome(entry.cwd) : undefined,
			env: entry.env,
		});
		try {
			const tools = await connection.start();
			connections.set(server, connection);
			for (const tool of tools) registerTool(server, threadArgs, tool);
		} catch (err) {
			connection.stop();
			throw new Error(`MCP server ${server}: ${(err as Error).message}`);
		}
	}

	function registerTool(server: string, threadArgs: string[], tool: McpTool): void {
		const name = `mcp__${server}__${tool.name}`;
		if (registered.has(name)) return;
		registered.add(name);
		pi.registerTool({
			name,
			label: `${server}: ${tool.name}`,
			description: tool.description ?? `${tool.name} from the ${server} MCP server`,
			parameters: (tool.inputSchema ?? { type: "object", properties: {} }) as any,
			async execute(_toolCallId, params, signal) {
				if (!connections.get(server)?.alive) await connect(server, threadArgs);
				const connection = connections.get(server);
				if (!connection) throw new Error(`MCP server ${server} is not running`);
				return toToolResult(await connection.call(tool.name, params, signal)) as any;
			},
		});
	}

	pi.on("session_start", async (_event, ctx: ExtensionContext) => {
		const value = pi.getFlag(MCP_ARGS_FLAG);
		if (typeof value !== "string" || value === "") return;
		try {
			for (const [server, threadArgs] of Object.entries(parseMcpArgs(value))) {
				if (!connections.get(server)?.alive) await connect(server, threadArgs);
			}
		} catch (err) {
			const message = (err as Error).message;
			if (ctx?.hasUI === true) ctx.ui.notify(`sesh: ${message}`, "error");
			else (dependencies.exit ?? exitLoudly)(message);
		}
	});

	pi.on("session_shutdown", async () => {
		for (const connection of connections.values()) connection.stop();
		connections.clear();
	});
}
