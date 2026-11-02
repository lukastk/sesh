package tui

import (
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// modelCell renders the MODEL column: the model the thread's transcript says it
// last ran on (owner-observed), which is the truth about what the agent is
// running. Before the first turn there is nothing observed, so the REGISTERED
// model shows in parentheses — what sesh will launch it with, not yet seen
// running. A trailing `*` marks an observed model that disagrees with the
// registered one (an in-session switch, or a registration the agent ignored).
// Blank = neither: an unpinned thread that has not taken a turn.
func modelCell(r api.ThreadRow) string {
	if r.ObservedModel == "" {
		if r.Model == "" {
			return ""
		}
		return "(" + shortModel(r.AgentKind, r.Model) + ")"
	}
	out := shortModel(r.AgentKind, r.ObservedModel)
	if !modelsAgree(r.AgentKind, r.Model, r.ObservedModel) {
		out += "*"
	}
	return out
}

// shortModel trims what the column does not need: pi's provider prefix
// (`anthropic/claude-haiku-4-5` -> `claude-haiku-4-5`; the full id is in
// `thread info`). Other harnesses' ids are already short.
func shortModel(agent, model string) string {
	if agent == "pi" || agent == "mypi" {
		if _, id, ok := strings.Cut(model, "/"); ok {
			return id
		}
	}
	return model
}

// claudeAliases are claude's own family aliases: `--model opus` runs the
// current opus, so any claude-opus-* model agrees with it.
var claudeAliases = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true}

// modelsAgree reports whether an observed model is what the registered one
// asks for, in each harness's own terms. Nothing registered agrees with
// anything (there is no intent to contradict).
func modelsAgree(agent, registered, observed string) bool {
	if registered == "" || registered == observed {
		return true
	}
	switch agent {
	case "claude":
		// The context-window suffix (`[1m]`) is a launch option, not part of the
		// model id claude records.
		reg, _, _ := strings.Cut(registered, "[")
		if reg == observed {
			return true
		}
		return claudeAliases[reg] && strings.HasPrefix(observed, "claude-"+reg+"-")
	case "pi", "mypi":
		// `provider/id[:thinking]`; the thinking level is not part of the model.
		reg, _, _ := strings.Cut(registered, ":")
		if reg == observed {
			return true
		}
		// A bare id (no provider) names the model in any provider.
		if !strings.Contains(reg, "/") {
			_, id, _ := strings.Cut(observed, "/")
			return reg == id
		}
	}
	return false
}
