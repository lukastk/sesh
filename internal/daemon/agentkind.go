package daemon

import "github.com/lukastk/sesh/internal/agents"

// The harness-kind predicates the daemon gates on. They exist because `mypi` is
// a second executable over ONE runtime shape: the patched pi fork writes pi's
// JSONL, opens pi's RPC sockets and answers pi's native metadata CLIs, while its
// pane process reports title "pi" or "mypi" depending on which packaging is
// installed (see agents.Family). Every gate that asks "is this a Pi thread?" is
// therefore really asking about the FAMILY, and every launch or native
// invocation must ask about the executable instead.
//
// A literal `AgentKind != "pi"` comparison at a native/socket gate would refuse
// every mypi thread with "unsupported_agent" even though the surface it guards
// works identically — and at a liveness comparison it would publish a live mypi
// pane as headless. Use these helpers at new call sites rather than string
// equality.

// piFamily reports whether a stored agent_kind is pi or mypi: one native
// conversation format, one socket protocol, one native CLI contract.
func piFamily(agentKind string) bool {
	return agents.Family(agents.Kind(agentKind)) == agents.Pi
}

// agentExecutable is the PATH-resolved executable for a stored agent_kind. A
// missing mypi fails loudly where it is invoked (exec.Error at a native read,
// command-not-found in a pane); it never degrades to pi.
func agentExecutable(agentKind string) string {
	return agents.Executable(agents.Kind(agentKind))
}

// paneAgentMatches compares a pane's DETECTED agent process against the kind a
// thread record claims, by family. See agents.PaneKindMatches for why a literal
// comparison would mark live mypi threads dead.
func paneAgentMatches(recordedKind, detectedKind string) bool {
	return agents.PaneKindMatches(agents.Kind(recordedKind), agents.Kind(detectedKind))
}
