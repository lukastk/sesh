/**
 * sesh thread context for pi (sesh schema 60): before every run, ask sesh for the
 * thread's custom context and put it in the system prompt as its own section
 * (`sesh_thread_context`). pi rebuilds the system prompt for each run and records
 * only what changed, so a context set, edited or removed while pi is running
 * applies from the next run, and a compacted session keeps it.
 *
 * `sesh context hook --agent pi --session <id>` does the deciding: it verifies
 * that this process is the thread's own agent (pane marker or headless turn, and
 * the thread's recorded session — a pi run NESTED inside the thread gets nothing),
 * then asks the owning daemon. It prints {"system": "...", "message": "..."}:
 * `system` is the section ("" = none), `message` a one-off notice when the
 * context changed within this session, added to the run so the model notices.
 *
 * Inert outside a sesh thread (no SESH_THREAD_ID). A failure never breaks pi: the
 * run proceeds without the context and the first failure is shown once.
 */

import { execFile } from "node:child_process";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

export const CONTEXT_SECTION = "sesh_thread_context";
export const CONTEXT_MESSAGE_TYPE = "sesh-thread-context";

export interface ContextAnswer {
	system: string;
	message: string;
}

export interface SeshThreadContextDependencies {
	threadID?: string;
	seshBin?: string;
	/** Test seam: run argv (after the binary) and resolve its stdout. */
	runCommand?: (argv: readonly string[]) => Promise<string>;
}

function runSesh(seshBin: string, argv: readonly string[]): Promise<string> {
	return new Promise((resolve, reject) => {
		execFile(seshBin, [...argv], { timeout: 10000, maxBuffer: 1 << 20 }, (err, stdout, stderr) => {
			if (err) {
				const detail = String(stderr || "").trim() || err.message;
				reject(new Error(detail));
				return;
			}
			resolve(String(stdout));
		});
	});
}

export function parseContextAnswer(stdout: string): ContextAnswer {
	const parsed = JSON.parse(stdout.trim() || "{}");
	return {
		system: typeof parsed.system === "string" ? parsed.system : "",
		message: typeof parsed.message === "string" ? parsed.message : "",
	};
}

export function registerSeshThreadContext(pi: ExtensionAPI, dependencies: SeshThreadContextDependencies = {}): void {
	const threadID = dependencies.threadID ?? process.env.SESH_THREAD_ID;
	if (!threadID) return; // not a sesh thread — stay inert
	const seshBin = dependencies.seshBin ?? (process.env.SESH_BIN || "sesh");
	const runCommand = dependencies.runCommand ?? ((argv) => runSesh(seshBin, argv));
	let warned = false;

	pi.on("before_agent_start", async (event: any, ctx: ExtensionContext) => {
		let answer: ContextAnswer;
		try {
			const sessionID = ctx.sessionManager.getSessionId();
			answer = parseContextAnswer(await runCommand(["context", "hook", "--agent", "pi", "--session", sessionID]));
		} catch (err) {
			// A sesh binary older than thread context (schema 60) has no `context`
			// command: it cannot hold a context for this thread, so there is nothing
			// to deliver and nothing to warn about while the fleet upgrades.
			if (/unknown command "context"/.test((err as Error).message)) return;
			if (!warned) {
				warned = true;
				try {
					ctx.ui?.notify?.(`sesh thread context unavailable: ${(err as Error).message}`, "warning");
				} catch {
					// no UI (print/RPC mode): nothing more to do
				}
			}
			return;
		}
		const options = event?.systemPromptOptions;
		if (options && answer.system) {
			options.sections = { ...(options.sections ?? {}), [CONTEXT_SECTION]: answer.system };
		}
		if (answer.message) {
			return { message: { customType: CONTEXT_MESSAGE_TYPE, content: answer.message, display: false } };
		}
	});
}
