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
 * live daemon and for isolated conformance-test daemons. Reports SERIALIZE
 * (one in flight, latest state queued behind it) so the CLI's
 * clock-at-invocation default seq is strictly increasing per thread.
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

const THREAD_ID = process.env.SESH_THREAD_ID;
// The spawning daemon's own binary (SESH_BIN): pane login shells re-prepend
// their profile dirs, so a bare PATH `sesh` can resolve to an OLDER installed
// binary than the daemon that spawned us. Fall back to PATH only when absent
// (adopted/pre-43 spawns) — a failure there stays visible as the thread
// remaining state_authority=heuristic.
const SESH_BIN = process.env.SESH_BIN || "sesh";
const SOURCE = "sesh:pi-ext";

type ReportEvent = "turn_started" | "turn_ended" | "release";

let inFlight = false;
let queued: ReportEvent | undefined;
let lastEnqueued: ReportEvent | undefined;

function send(ev: ReportEvent): Promise<void> {
	return new Promise((resolve) => {
		execFile(
			SESH_BIN,
			[
				"thread",
				"report-state",
				"--id",
				THREAD_ID as string,
				"--source",
				SOURCE,
				"--event",
				ev,
			],
			{ timeout: 5000 },
			() => resolve(), // fail-silent: never break pi over a report
		);
	});
}

async function drain(): Promise<void> {
	if (inFlight) return;
	inFlight = true;
	try {
		while (queued !== undefined) {
			const ev = queued;
			queued = undefined;
			await send(ev);
		}
	} finally {
		inFlight = false;
		if (queued !== undefined) void drain();
	}
}

/** Queue the CURRENT desired state (latest wins; duplicate states dedupe). */
function report(ev: ReportEvent): void {
	if (!THREAD_ID) return;
	if (ev === lastEnqueued) return;
	lastEnqueued = ev;
	queued = ev;
	void drain();
}

export default function (pi: ExtensionAPI) {
	if (!THREAD_ID) return; // not a sesh thread — stay inert

	// Only the root interactive session reports: nested/headless pi runs have
	// no UI, and a sesh HEADLESS turn's busy already comes from the daemon's
	// own turn registry (reports there would just be cleared every tick).
	let rootSession = false;

	pi.on("session_start", async (_event, ctx: ExtensionContext) => {
		if (ctx?.hasUI !== true) return;
		rootSession = true;
		// Re-derive rather than assume idle: a runtime reload mid-turn emits
		// session_start with the agent still running and no agent_start after.
		report(ctx.isIdle?.() === false ? "turn_started" : "turn_ended");
	});

	pi.on("agent_start", async () => {
		if (!rootSession) return;
		report("turn_started");
	});

	pi.on("agent_settled", async (_event, ctx: ExtensionContext) => {
		if (!rootSession || ctx?.isIdle?.() !== true) return;
		report("turn_ended");
	});

	pi.on("session_shutdown", async (event: { reason?: string }) => {
		if (!rootSession || event?.reason !== "quit") return;
		// Await directly (not via the queue): pi is exiting and a queued
		// fire-and-forget would race process teardown. The pane-liveness bound
		// clears authority anyway when the pane dies; this is belt-and-braces.
		queued = undefined;
		await send("release");
	});
}
