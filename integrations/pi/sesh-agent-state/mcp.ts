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
 * The client stays live. The tool list follows `notifications/tools/list_changed`:
 * tools appear, change and are withdrawn in the running process. The server's
 * `instructions` reach the model as a system prompt section on every run.
 *
 * Failure is loud. A headless turn exits non-zero before the model runs, because a
 * worker without its tools would otherwise answer without them. That covers a server
 * that cannot start, and one that promises list changes but still lists nothing after
 * a bounded wait. A headed pi shows the error, tells the model the server is down, and
 * tries the start again at each run and each tool call.
 *
 * A tool call has no time limit unless the entry sets `timeout`, in seconds, reset by
 * progress notifications. An abort or a timeout sends `notifications/cancelled`, but a
 * server may not honour it, so the error says the call may still be running.
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
// How long a headless run waits for a server that listed nothing but promised
// changes. Longer than the 15s at which soliton-mcp polls its workspace.
const TOOLS_WAIT_MS = 45_000;
const TOOLS_WAIT_POLL_MS = 100;

export interface McpEntry {
	command: string;
	args?: string[];
	env?: Record<string, string>;
	cwd?: string;
	/** Seconds a tool call may go without an answer or progress. Unset means no limit. */
	timeout?: number;
}

export interface McpTool {
	name: string;
	description?: string;
	inputSchema?: Record<string, unknown>;
}

export interface McpConnectionOptions {
	cwd?: string;
	env?: Record<string, string>;
	/** The limit on protocol requests, not on tool calls. */
	timeoutMs?: number;
	/** The limit on a tool call. Unset means none. */
	callTimeoutMs?: number;
	onToolsChanged?: () => void;
}

interface RequestOptions {
	signal?: AbortSignal;
	timeoutMs?: number;
	/** A tool call: announce a progress token, and ask the server to cancel on abort or timeout. */
	cancellable?: boolean;
}

interface PendingRequest {
	resolve: (value: any) => void;
	reject: (err: Error) => void;
	/** Progress arrived: the limit starts again. */
	touch: () => void;
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
	if (entry.timeout !== undefined && !(typeof entry.timeout === "number" && entry.timeout > 0)) {
		throw new Error(`the global MCP server "${server}" in ${path} has a timeout that is not a positive number of seconds`);
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
	private pending = new Map<number, PendingRequest>();
	private exitError: Error | undefined;
	private serverInstructions = "";
	private toolsCanChange = false;

	private readonly command: string;
	private readonly args: readonly string[];
	private readonly options: McpConnectionOptions;

	constructor(command: string, args: readonly string[], options: McpConnectionOptions) {
		this.command = command;
		this.args = args;
		this.options = options;
	}

	get alive(): boolean {
		return this.child !== undefined && this.exitError === undefined;
	}

	/** What the server said at `initialize`. MCP has no way to update it afterwards. */
	get instructions(): string {
		return this.serverInstructions;
	}

	/** Whether the server promised `notifications/tools/list_changed`. */
	get listChanged(): boolean {
		return this.toolsCanChange;
	}

	get stderr(): string {
		return this.stderrTail.trim();
	}

	/** Starts the server and completes the handshake. */
	async start(): Promise<void> {
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
		const init = await this.request("initialize", {
			protocolVersion: PROTOCOL_VERSION,
			capabilities: {},
			clientInfo: { name: "sesh-agent-state", version: "1" },
		});
		this.serverInstructions = typeof init.instructions === "string" ? init.instructions : "";
		this.toolsCanChange = init.capabilities?.tools?.listChanged === true;
		this.send({ jsonrpc: "2.0", method: "notifications/initialized" });
	}

	async listTools(): Promise<McpTool[]> {
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
		return this.request("tools/call", { name: tool, arguments: args ?? {} }, { signal, timeoutMs: this.options.callTimeoutMs, cancellable: true });
	}

	stop(): void {
		this.child?.kill();
		this.fail(new Error("stopped"));
	}

	private send(message: unknown): void {
		this.child?.stdin?.write(`${JSON.stringify(message)}\n`);
	}

	private request(method: string, params: any, options: RequestOptions = { timeoutMs: this.options.timeoutMs ?? REQUEST_TIMEOUT_MS }): Promise<any> {
		if (this.exitError) return Promise.reject(this.exitError);
		const { signal, timeoutMs, cancellable } = options;
		const id = this.nextId++;
		return new Promise((resolve, reject) => {
			if (signal?.aborted) {
				reject(new Error("aborted before it was sent"));
				return;
			}
			let timer: NodeJS.Timeout | undefined;
			const settle = (run: () => void) => {
				clearTimeout(timer);
				signal?.removeEventListener("abort", onAbort);
				this.pending.delete(id);
				run();
			};
			// The answer may never come, or come too late to matter: either way the call
			// is no longer known to have failed, and a caller must not assume it did.
			const giveUp = (reason: string) =>
				settle(() => {
					if (cancellable) this.send({ jsonrpc: "2.0", method: "notifications/cancelled", params: { requestId: id, reason } });
					reject(new Error(cancellable ? `${method}: ${reason}. The server was asked to cancel, but the call may still be running: check its effect before repeating it` : `${method}: ${reason}`));
				});
			const onAbort = () => giveUp("aborted");
			const arm = () => {
				if (timeoutMs === undefined) return;
				clearTimeout(timer);
				timer = setTimeout(() => giveUp(`no answer in ${timeoutMs}ms`), timeoutMs);
			};
			arm();
			signal?.addEventListener("abort", onAbort, { once: true });
			this.pending.set(id, {
				resolve: (value) => settle(() => resolve(value)),
				reject: (err) => settle(() => reject(err)),
				touch: arm,
			});
			this.send({ jsonrpc: "2.0", id, method, params: cancellable ? { ...params, _meta: { progressToken: id } } : params });
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
		} else if (message.method === "notifications/tools/list_changed") {
			this.options.onToolsChanged?.();
		} else if (message.method === "notifications/progress") {
			this.pending.get(message.params?.progressToken)?.touch();
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

/** A provider accepts these characters in a tool name. The server still gets its own name. */
export function piToolName(server: string, tool: string): string {
	return `mcp__${server}__${tool.replace(/[^a-zA-Z0-9_-]/g, "_")}`;
}

export interface SeshMcpDependencies {
	dir?: string;
	/** Test seam for the failure path of a headless run. */
	exit?: (message: string) => void;
	/** Test seam for how long a headless run waits for a first tool. */
	toolsWaitMs?: number;
}

/** One server this thread named: its running connection, and what it has offered. */
interface ServerState {
	name: string;
	threadArgs: string[];
	connection?: McpConnection;
	starting?: Promise<void>;
	syncing?: Promise<void>;
	syncAgain?: boolean;
	/** Every tool ever registered, by pi name. pi cannot unregister one. */
	tools: Map<string, { definition: string; listed: boolean }>;
	/** Why the server is down or its list could not be read; undefined when it is well. */
	error?: string;
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

	const servers = new Map<string, ServerState>();

	function ensureRunning(state: ServerState): Promise<void> {
		state.starting ??= state.connection?.alive
			? undefined
			: connect(state).finally(() => {
					state.starting = undefined;
				});
		return state.starting ?? Promise.resolve();
	}

	async function connect(state: ServerState): Promise<void> {
		try {
			const entry = readGlobalEntry(dependencies.dir ?? agentDir(), state.name);
			const connection: McpConnection = new McpConnection(entry.command, [...(entry.args ?? []), ...state.threadArgs], {
				cwd: entry.cwd ? expandHome(entry.cwd) : undefined,
				env: entry.env,
				callTimeoutMs: entry.timeout === undefined ? undefined : entry.timeout * 1000,
				onToolsChanged: () => {
					sync(state, connection).catch((err) => {
						state.error = `its tool list could not be read: ${(err as Error).message}`;
					});
				},
			});
			state.connection = connection;
			try {
				await connection.start();
				await sync(state, connection);
			} catch (err) {
				connection.stop();
				throw err;
			}
			state.error = undefined;
		} catch (err) {
			state.error = (err as Error).message;
			throw new Error(`MCP server ${state.name}: ${state.error}`);
		}
	}

	/** Brings the registered tools up to the server's list, one reading at a time. */
	function sync(state: ServerState, connection: McpConnection): Promise<void> {
		if (state.syncing) {
			state.syncAgain = true;
			return state.syncing;
		}
		state.syncing = (async () => {
			do {
				state.syncAgain = false;
				const tools = await connection.listTools();
				if (state.connection !== connection) return;
				applyTools(state, tools);
				state.error = undefined;
			} while (state.syncAgain);
		})().finally(() => {
			state.syncing = undefined;
		});
		return state.syncing;
	}

	function applyTools(state: ServerState, tools: McpTool[]): void {
		const listed = new Set<string>();
		const restored: string[] = [];
		for (const tool of tools) {
			const name = piToolName(state.name, tool.name);
			if (listed.has(name)) throw new Error(`two tools share the name ${name}`);
			listed.add(name);
			const definition = JSON.stringify(tool);
			const known = state.tools.get(name);
			if (known?.definition !== definition) registerTool(state, name, tool);
			if (known && !known.listed) restored.push(name);
			state.tools.set(name, { definition, listed: true });
		}
		const withdrawn: string[] = [];
		for (const [name, known] of state.tools) {
			if (known.listed && !listed.has(name)) {
				known.listed = false;
				withdrawn.push(name);
			}
		}
		if (withdrawn.length > 0 || restored.length > 0) {
			const active = pi.getActiveTools().filter((name) => !withdrawn.includes(name));
			pi.setActiveTools([...new Set([...active, ...restored])]);
		}
	}

	function registerTool(state: ServerState, name: string, tool: McpTool): void {
		pi.registerTool({
			name,
			label: `${state.name}: ${tool.name}`,
			description: tool.description ?? `${tool.name} from the ${state.name} MCP server`,
			parameters: (tool.inputSchema ?? { type: "object", properties: {} }) as any,
			async execute(_toolCallId, params, signal) {
				await ensureRunning(state);
				if (!state.connection) throw new Error(`MCP server ${state.name} is not running`);
				return toToolResult(await state.connection.call(tool.name, params, signal)) as any;
			},
		});
	}

	function listsTools(state: ServerState): boolean {
		return [...state.tools.values()].some((tool) => tool.listed);
	}

	/** Returns once `state` lists a tool, its server has died, or the wait is over. */
	async function waitForTools(state: ServerState): Promise<void> {
		const deadline = Date.now() + (dependencies.toolsWaitMs ?? TOOLS_WAIT_MS);
		while (!listsTools(state) && state.connection?.alive && Date.now() < deadline) {
			await new Promise((resolve) => setTimeout(resolve, TOOLS_WAIT_POLL_MS));
		}
	}

	function fail(ctx: ExtensionContext, message: string): void {
		if (ctx?.hasUI === true) ctx.ui.notify(`sesh: ${message}`, "error");
		else (dependencies.exit ?? exitLoudly)(message);
	}

	pi.on("session_start", async (_event, ctx: ExtensionContext) => {
		const value = pi.getFlag(MCP_ARGS_FLAG);
		if (typeof value !== "string" || value === "") return;
		let parsed: Record<string, string[]>;
		try {
			parsed = parseMcpArgs(value);
		} catch (err) {
			fail(ctx, (err as Error).message);
			return;
		}
		for (const [name, threadArgs] of Object.entries(parsed)) {
			if (!servers.has(name)) servers.set(name, { name, threadArgs, tools: new Map() });
			try {
				await ensureRunning(servers.get(name) as ServerState);
			} catch (err) {
				fail(ctx, (err as Error).message);
			}
		}
	});

	// Every run is a chance to recover: a server that failed gets another start, and a
	// worker with nobody watching its pane hears about what is still wrong.
	pi.on("before_agent_start", async (event: any, ctx: ExtensionContext) => {
		for (const state of servers.values()) {
			const key = `sesh_mcp_${state.name}`;
			let down: string | undefined;
			try {
				await ensureRunning(state);
			} catch (err) {
				down = (err as Error).message;
				if (ctx?.hasUI !== true) fail(ctx, down);
			}
			const connection = state.connection;
			if (ctx?.hasUI !== true && !down && connection?.listChanged && !listsTools(state)) {
				await waitForTools(state);
				if (!listsTools(state)) {
					const said = [connection.instructions, connection.stderr].filter(Boolean).join(" ");
					down = `MCP server ${state.name} still lists no tools${said ? `: ${said}` : ""}`;
					fail(ctx, down);
				}
			}
			const options = event?.systemPromptOptions;
			if (!options) continue;
			const problem = down ?? (state.error ? `MCP server ${state.name}: ${state.error}` : undefined);
			const parts = [
				connection?.alive && connection.instructions ? `Instructions from the ${state.name} MCP server:\n${connection.instructions}` : "",
				problem ? `The tools of the ${state.name} MCP server are unavailable. Say so rather than working around it. ${problem}` : "",
			].filter(Boolean);
			if (parts.length > 0) options.sections = { ...(options.sections ?? {}), [key]: parts.join("\n\n") };
		}
	});

	pi.on("session_shutdown", async () => {
		for (const state of servers.values()) {
			state.connection?.stop();
			state.connection = undefined;
		}
		servers.clear();
	});
}
