/**
 * sesh-agent-state — a pi extension that reports turn lifecycle to the sesh
 * daemon, giving sesh EXACT busy/idle state for pi threads instead of the pane
 * content-diff heuristic (schema 43, issue #4 — see _dev/STATE_AUTHORITY.md).
 *
 * Provisioning: this file lives in the sesh repo and is registered as an
 * external extension via myagent (a symlink in ~/.pi/agent/extensions/). It is
 * INERT outside a sesh thread: without SESH_THREAD_ID in the environment it
 * registers nothing.
 *
 * Transport: each report shells out to `sesh thread report-state` (argv-only,
 * nothing interpolated into a shell string) — the CLI resolves the daemon
 * socket from the inherited environment, so this works identically for the
 * live daemon and for isolated conformance-test daemons. Every report carries
 * the session id captured from that lifecycle callback's current context.
 * Reports serialize (one in flight, latest payload queued behind it), so a
 * replacement session can never be retargeted through a stale context closure.
 *
 * Event mapping (see pi's docs/extensions.md lifecycle):
 * - agent_start            -> turn_started
 * - agent_settled + isIdle -> turn_ended (agent_end is NOT enough: pi may
 *                             auto-retry/compact/continue after it)
 * - session_start (hasUI)  -> re-derive from ctx.isIdle(): a /reload, /new,
 *                             /resume or /fork replaces this extension runtime
 *                             MID-RUN without another agent_start.
 * - session_shutdown       -> release, ONLY for reason "quit": the other
 *                             reasons (reload/new/resume/fork) keep the agent
 *                             process alive and a release would suppress the
 *                             replacement runtime's reports.
 *
 * Failures are silent toward pi (a reporting problem must never break the
 * agent); the daemon side stays honest because a thread with no live reports
 * simply remains on the heuristic floor, visibly (state_authority).
 */

import { execFile } from "node:child_process";
import type {
	ExtensionAPI,
	ExtensionContext,
} from "@earendil-works/pi-coding-agent";

const DEFAULT_THREAD_ID = process.env.SESH_THREAD_ID;
// The spawning daemon's own binary (SESH_BIN): pane login shells re-prepend
// their profile dirs, so a bare PATH `sesh` can resolve to an OLDER installed
// binary than the daemon that spawned us. Fall back to PATH only when absent
// (adopted/pre-43 spawns) — a failure there stays visible as the thread
// remaining state_authority=heuristic.
const DEFAULT_SESH_BIN = process.env.SESH_BIN || "sesh";
const SOURCE = "sesh:pi-ext";

type ReportEvent = "turn_started" | "turn_ended" | "release";

interface ReportPayload {
	event: ReportEvent;
	agentSessionID: string;
}

export interface SeshAgentStateDependencies {
	threadID?: string;
	seshBin?: string;
	/** Test seam: argv after the binary, executed without a shell. */
	runCommand?: (argv: readonly string[]) => Promise<void>;
}

function runSeshCommand(seshBin: string, argv: readonly string[]): Promise<void> {
	return new Promise((resolve) => {
		execFile(
			seshBin,
			[...argv],
			{ timeout: 5000 },
			() => resolve(), // fail-silent: never break pi over a report
		);
	});
}

/** Build one isolated extension runtime. Exported only for source-level tests. */
export function createSeshAgentStateExtension(
	dependencies: SeshAgentStateDependencies = {},
): (pi: ExtensionAPI) => void {
	const threadID = dependencies.threadID ?? DEFAULT_THREAD_ID;
	const seshBin = dependencies.seshBin ?? DEFAULT_SESH_BIN;
	const runCommand = dependencies.runCommand ?? ((argv) => runSeshCommand(seshBin, argv));
	let queued: ReportPayload | undefined;
	let inFlight: Promise<void> | undefined;
	let lastEnqueuedKey: string | undefined;

	async function send(payload: ReportPayload): Promise<void> {
		const argv = [
			"thread",
			"report-state",
			"--id",
			threadID as string,
			"--source",
			SOURCE,
			"--event",
			payload.event,
			"--agent-session",
			payload.agentSessionID,
		];
		try {
			await runCommand(argv);
		} catch {
			// An injected/test runner may reject. Production execFile failures resolve
			// through the callback. Either way, reporting never breaks native Pi.
		}
	}

	async function drain(): Promise<void> {
		while (queued !== undefined) {
			const payload = queued;
			queued = undefined;
			await send(payload);
		}
	}

	function ensureDrain(): Promise<void> {
		if (inFlight === undefined) {
			inFlight = drain().finally(() => {
				inFlight = undefined;
				if (queued !== undefined) void ensureDrain();
			});
		}
		return inFlight;
	}

	/** Queue the current desired state and its callback-captured session identity. */
	function report(ev: ReportEvent, ctx: ExtensionContext): Promise<void> {
		if (!threadID) return Promise.resolve();
		const payload: ReportPayload = {
			event: ev,
			agentSessionID: ctx.sessionManager.getSessionId(),
		};
		const key = `${payload.event}\u0000${payload.agentSessionID}`;
		if (key === lastEnqueuedKey) return inFlight ?? Promise.resolve();
		lastEnqueuedKey = key;
		queued = payload;
		return ensureDrain();
	}

	return function register(pi: ExtensionAPI) {
		if (!threadID) return; // not a sesh thread — stay inert

		// Only the root interactive session reports: nested/headless pi runs have
		// no UI, and a sesh HEADLESS turn's busy already comes from the daemon's
		// own turn registry (reports there would just be cleared every tick).
		let rootSession = false;

		pi.on("session_start", async (_event, ctx: ExtensionContext) => {
			if (ctx?.hasUI !== true) return;
			rootSession = true;
			// Session replacement rotates Pi's session-owned sockets. Await this local,
			// bounded, fail-silent binding so Sesh records the exact current identity
			// before session startup is considered complete.
			await report(ctx.isIdle?.() === false ? "turn_started" : "turn_ended", ctx);
		});

		pi.on("agent_start", async (_event, ctx: ExtensionContext) => {
			if (!rootSession) return;
			// Re-capture on every run: Pi may replace stale saved session references.
			await report("turn_started", ctx);
		});

		pi.on("agent_settled", async (_event, ctx: ExtensionContext) => {
			if (!rootSession || ctx?.isIdle?.() !== true) return;
			await report("turn_ended", ctx);
		});

		pi.on("session_shutdown", async (event: { reason?: string }, ctx: ExtensionContext) => {
			if (!rootSession || event?.reason !== "quit") return;
			// Queue + await release behind any in-flight state report. This preserves
			// report ordering while process teardown is waiting on the handler.
			await report("release", ctx);
		});
	};
}

export default function (pi: ExtensionAPI) {
	createSeshAgentStateExtension()(pi);
}
