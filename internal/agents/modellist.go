package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ModelOption is one model a harness reports it can use on this machine.
type ModelOption struct {
	// ID is what --model accepts, in the harness's own spelling.
	ID string
	// Label is the harness's display name ('' when it has none).
	Label string
	// Description is the harness's one-line blurb ('' when it has none).
	Description string
	// Resolved is the concrete model an alias stands for (claude `opus` ->
	// `claude-opus-5-5`); '' when ID is already concrete.
	Resolved string
}

// ModelList is a harness's usable models plus, where the harness says so, the
// concrete model it runs when given no --model.
type ModelList struct {
	Models         []ModelOption
	HarnessDefault string
}

// ListModels asks the harness itself which models this machine can use. Each
// harness has its own source, and none is a list sesh maintains:
//
//   - claude has no listing command. Its SDK control protocol does: one
//     `initialize` control request piped into `claude -p --input-format
//     stream-json` answers with the account's model options and exits at EOF,
//     without running a model turn or persisting a session.
//   - codex: `codex debug models` prints its catalog as JSON; only entries with
//     visibility "list" are offered (the others are internal). It sits under
//     `debug`, so a shape change is a loud parse error, not an empty list.
//   - pi / mypi: `--list-models` prints a table of the models of every provider
//     the user has credentials for.
//
// env is the full environment for the child (the caller injects CODEX_HOME etc.);
// dir is its working directory. An empty list is an error: a harness that can
// use no model at all is misconfigured, and a picker must not show "nothing".
func ListModels(ctx context.Context, k Kind, env []string, dir string) (ModelList, error) {
	var (
		cmd   *exec.Cmd
		stdin string
	)
	switch k {
	case Claude:
		cmd = exec.CommandContext(ctx, Executable(k), "-p", "--input-format", "stream-json",
			"--output-format", "stream-json", "--verbose", "--no-session-persistence")
		stdin = `{"type":"control_request","request_id":"sesh-models","request":{"subtype":"initialize"}}` + "\n"
	case Codex:
		cmd = exec.CommandContext(ctx, Executable(k), "debug", "models")
	case Pi, MyPi:
		cmd = exec.CommandContext(ctx, Executable(k), "--list-models")
	default:
		return ModelList{}, fmt.Errorf("list models: unknown agent %q", k)
	}
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return ModelList{}, fmt.Errorf("list %s models: %s: %w: %s", k, strings.Join(cmd.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	var (
		out ModelList
		err error
	)
	switch k {
	case Claude:
		out, err = parseClaudeInitModels(stdout.Bytes())
	case Codex:
		out, err = parseCodexModels(stdout.Bytes())
	default:
		out, err = parsePiModels(stdout.Bytes())
	}
	if err != nil {
		return ModelList{}, fmt.Errorf("list %s models: %w", k, err)
	}
	if len(out.Models) == 0 {
		return ModelList{}, fmt.Errorf("list %s models: the harness reported no usable models", k)
	}
	return out, nil
}

// parseClaudeInitModels reads the stream-json control_response to the
// `initialize` request. Its "default" entry is not a model but "use the
// account default": it is reported as HarnessDefault (its resolved model), not
// offered as an option.
func parseClaudeInitModels(stream []byte) (ModelList, error) {
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		var msg struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Error     string `json:"error"`
				Response  struct {
					Models []struct {
						Value         string `json:"value"`
						ResolvedModel string `json:"resolvedModel"`
						DisplayName   string `json:"displayName"`
						Description   string `json:"description"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.Type != "control_response" || msg.Response.RequestID != "sesh-models" {
			continue
		}
		if msg.Response.Subtype != "success" {
			return ModelList{}, fmt.Errorf("initialize was refused: %s", msg.Response.Error)
		}
		var out ModelList
		for _, m := range msg.Response.Response.Models {
			if m.Value == "" {
				return ModelList{}, fmt.Errorf("initialize listed a model with no value: %+v", m)
			}
			if m.Value == "default" {
				out.HarnessDefault = m.ResolvedModel
				continue
			}
			resolved := m.ResolvedModel
			if resolved == m.Value {
				resolved = ""
			}
			out.Models = append(out.Models, ModelOption{ID: m.Value, Label: m.DisplayName, Description: m.Description, Resolved: resolved})
		}
		return out, nil
	}
	if err := sc.Err(); err != nil {
		return ModelList{}, err
	}
	return ModelList{}, fmt.Errorf("no control_response to the initialize request in claude's output")
}

func parseCodexModels(raw []byte) (ModelList, error) {
	var cat struct {
		Models *[]struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Description string `json:"description"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		return ModelList{}, fmt.Errorf("parse `codex debug models`: %w", err)
	}
	if cat.Models == nil {
		return ModelList{}, fmt.Errorf("`codex debug models` has no \"models\" array (its format changed?)")
	}
	var out ModelList
	for _, m := range *cat.Models {
		if m.Slug == "" {
			return ModelList{}, fmt.Errorf("`codex debug models` listed a model with no slug")
		}
		if m.Visibility != "list" {
			continue
		}
		out.Models = append(out.Models, ModelOption{ID: m.Slug, Label: m.DisplayName, Description: m.Description})
	}
	return out, nil
}

// parsePiModels reads `pi --list-models`: a whitespace-aligned table headed
// `provider model context max-out thinking images`. The id --model takes is
// provider/model.
func parsePiModels(raw []byte) (ModelList, error) {
	var out ModelList
	header := false
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if !header {
			if len(f) >= 2 && f[0] == "provider" && f[1] == "model" {
				header = true
			}
			continue
		}
		if len(f) < 2 {
			return ModelList{}, fmt.Errorf("unexpected `--list-models` row %q", line)
		}
		out.Models = append(out.Models, ModelOption{ID: f[0] + "/" + f[1]})
	}
	if !header {
		return ModelList{}, fmt.Errorf("no `provider model …` header in `--list-models` output (its format changed?)")
	}
	return out, nil
}
