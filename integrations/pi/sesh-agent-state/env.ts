/**
 * sesh thread env for pi (sesh schema 62): before every run, bring process.env in
 * line with the thread's variables. pi's shell tool inherits process.env, so a
 * variable set, changed or removed while pi is running reaches its shell commands
 * from the next run. Provider settings pi read at startup are not re-read: those
 * need `sesh thread restart`.
 *
 * The daemon names the thread's env.sh in SESH_THREAD_ENV_FILE at every launch, and
 * env.json sits beside it. A headless turn is a fresh process launched with the
 * variables, so it has no such file to follow. Inert without the variable. A failure
 * never breaks pi: the run proceeds and the first failure is shown once.
 */

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

export interface ThreadEnv {
	vars: Record<string, string>;
	removed: string[];
}

export function parseThreadEnv(raw: string): ThreadEnv {
	const parsed = JSON.parse(raw);
	return { vars: parsed.vars ?? {}, removed: parsed.removed ?? [] };
}

/** Applies a thread env to `target`: removals first, as env.sh does. */
export function applyThreadEnv(target: Record<string, string | undefined>, env: ThreadEnv): void {
	for (const key of env.removed) delete target[key];
	Object.assign(target, env.vars);
}

export interface SeshThreadEnvDependencies {
	envFile?: string;
	/** Test seam: the environment to update. */
	target?: Record<string, string | undefined>;
	readFile?: (path: string) => string;
}

export function registerSeshThreadEnv(pi: ExtensionAPI, dependencies: SeshThreadEnvDependencies = {}): void {
	const envFile = dependencies.envFile ?? process.env.SESH_THREAD_ENV_FILE;
	if (!envFile) return; // not launched by sesh — stay inert
	const target = dependencies.target ?? process.env;
	const readFile = dependencies.readFile ?? ((path) => readFileSync(path, "utf8"));
	const jsonFile = join(dirname(envFile), "env.json");
	let warned = false;

	pi.on("before_agent_start", async (_event: any, ctx: ExtensionContext) => {
		try {
			applyThreadEnv(target, parseThreadEnv(readFile(jsonFile)));
		} catch (err) {
			if (warned) return;
			warned = true;
			try {
				ctx.ui?.notify?.(`sesh thread env unavailable: ${(err as Error).message}`, "warning");
			} catch {
				// no UI (print/RPC mode): nothing more to do
			}
		}
	});
}
