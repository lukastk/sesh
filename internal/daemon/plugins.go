package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// pluginRunTimeout bounds a single plugin command so a hung command cannot wedge the
// daemon. Generous: boxyard list over a network store can be slow.
const pluginRunTimeout = 60 * time.Second

// routesPlugins serves the daemon command-provider substrate: GET /v1/plugins lists
// every manifest at <SESH_HOME>/plugins/*.toml, and POST /v1/plugins/{name}/{capability}
// runs the configured command ON THIS DAEMON'S HOST. On the shared router these are
// automatically exposed over the TCP API behind the bearer token AND routed
// cross-machine (like fs/list), so the app can reach any machine's plugins.
//
// SECURITY (single-user, all on tailscale — sandboxing is N/A by design): the command
// comes from the manifest ONLY, never the client. Action field values are substituted
// as ARGV (exec without a shell) so a value like "; rm -rf ~" is a literal argument,
// never interpreted.
func (d *Daemon) routesPlugins(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/plugins", d.handlePluginsList)
	mux.HandleFunc("POST /v1/plugins/{name}/{capability}", d.handlePluginRun)
}

func (d *Daemon) handlePluginsList(w http.ResponseWriter, r *http.Request) {
	specs, err := config.LoadPlugins(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.PluginsListResponse{Schema: api.SchemaVersion}
	for _, s := range specs {
		resp.Plugins = append(resp.Plugins, toAPIPlugin(s))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *Daemon) handlePluginRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	capName := r.PathValue("capability")
	specs, err := config.LoadPlugins(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var plugin *config.PluginSpec
	for i := range specs {
		if specs[i].Name == name {
			plugin = &specs[i]
			break
		}
	}
	if plugin == nil {
		writeError(w, http.StatusNotFound, "plugin not found: "+name)
		return
	}

	// The body (field values) is optional — a list capability takes none.
	var req api.PluginRunRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// A list capability?
	for _, l := range plugin.Lists {
		if l.Name == capName {
			d.runPluginList(w, r.Context(), name, l, req)
			return
		}
	}
	// An action capability?
	for _, a := range plugin.Actions {
		if a.Name == capName {
			d.runPluginAction(w, r.Context(), name, a, req)
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("plugin %q has no capability %q", name, capName))
}

// runPluginList runs a list capability's command, parses its JSON stdout, navigates to
// the items array and maps each item to {id,label,groups,path} via the configured
// templates. Every step that can disagree with the manifest fails LOUDLY.
func (d *Daemon) runPluginList(w http.ResponseWriter, parent context.Context, plugin string, l config.PluginListSpec, req api.PluginRunRequest) {
	if len(req.Fields) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("plugin %s/%s: a list capability takes no fields", plugin, l.Name))
		return
	}
	stdout, stderr, err := d.runPluginCommand(parent, l.Command)
	if err != nil {
		writeError(w, http.StatusInternalServerError, pluginCmdError(plugin, l.Name, err, stderr))
		return
	}
	var root any
	if err := json.Unmarshal([]byte(stdout), &root); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("plugin %s/%s: command output is not JSON: %v", plugin, l.Name, err))
		return
	}
	arr, err := navigateToArray(root, l.Items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("plugin %s/%s: %v", plugin, l.Name, err))
		return
	}
	items := make([]api.PluginListItem, 0, len(arr))
	for idx, raw := range arr {
		obj, ok := raw.(map[string]any)
		if !ok {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("plugin %s/%s: item %d is not a JSON object", plugin, l.Name, idx))
			return
		}
		id, err := renderItemTemplate(l.ID, obj)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("plugin %s/%s item %d id: %v", plugin, l.Name, idx, err))
			return
		}
		label, err := renderItemTemplate(l.Label, obj)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("plugin %s/%s item %d label: %v", plugin, l.Name, idx, err))
			return
		}
		item := api.PluginListItem{ID: id, Label: label}
		if l.Path != "" {
			p, err := renderItemTemplate(l.Path, obj)
			if err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("plugin %s/%s item %d path: %v", plugin, l.Name, idx, err))
				return
			}
			item.Path = p
		}
		if l.Groups != "" {
			groups, err := itemGroups(obj, l.Groups)
			if err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("plugin %s/%s item %d groups: %v", plugin, l.Name, idx, err))
				return
			}
			item.Groups = groups
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, api.PluginRunResponse{
		Schema: api.SchemaVersion, Plugin: plugin, Capability: l.Name, Kind: "list", Items: items,
	})
}

// runPluginAction substitutes the form field values into the command argv (ARGV, no
// shell) and runs it. A missing required field or an unknown supplied field is a loud
// 400; a nonzero exit is a loud 500 carrying stderr — never a silent success.
func (d *Daemon) runPluginAction(w http.ResponseWriter, parent context.Context, plugin string, a config.PluginActionSpec, req api.PluginRunRequest) {
	declared := map[string]config.PluginFieldSpec{}
	for _, f := range a.Fields {
		declared[f.Name] = f
	}
	// Reject unknown fields LOUDLY — no silently-ignored input.
	for name := range req.Fields {
		if _, ok := declared[name]; !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("plugin %s/%s: unknown field %q", plugin, a.Name, name))
			return
		}
	}
	// A required field with no value is a loud 400.
	for _, f := range a.Fields {
		if f.Required {
			if _, ok := req.Fields[f.Name]; !ok {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("plugin %s/%s: required field %q is missing", plugin, a.Name, f.Name))
				return
			}
		}
	}
	argv := make([]string, len(a.Command))
	for i, arg := range a.Command {
		argv[i] = placeholderRe.ReplaceAllStringFunc(arg, func(ph string) string {
			key := ph[1 : len(ph)-1] // strip { }
			return req.Fields[key]   // validated declared; empty for an unset optional field
		})
	}
	stdout, stderr, err := d.runPluginCommand(parent, argv)
	if err != nil {
		writeError(w, http.StatusInternalServerError, pluginCmdError(plugin, a.Name, err, stderr))
		return
	}
	writeJSON(w, http.StatusOK, api.PluginRunResponse{
		Schema: api.SchemaVersion, Plugin: plugin, Capability: a.Name, Kind: "action", Output: stdout,
	})
}

// runPluginCommand execs argv on the daemon's host with the daemon's environment (so
// PATH/mise shims resolve, exactly as the deploy ini provisions), cwd = the daemon
// user's home. NO shell is involved: argv[0] is resolved via PATH and the args are
// passed verbatim, so field values cannot inject. Bounded by pluginRunTimeout.
func (d *Daemon) runPluginCommand(parent context.Context, argv []string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(parent, pluginRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		cmd.Dir = home
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func pluginCmdError(plugin, capability string, err error, stderr string) string {
	msg := fmt.Sprintf("plugin %s/%s: command failed: %v", plugin, capability, err)
	if s := strings.TrimSpace(stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

// placeholderRe matches a "{ident}" template placeholder — the SAME token set
// config validation enforces (config.placeholderRe in naming.go).
var placeholderRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// navigateToArray descends a dotted path through nested JSON objects to the array a
// list capability maps. An empty path means the root itself is the array.
func navigateToArray(root any, path string) ([]any, error) {
	cur := root
	if path != "" {
		for _, key := range strings.Split(path, ".") {
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("items path %q: %q is not an object", path, key)
			}
			next, ok := obj[key]
			if !ok {
				return nil, fmt.Errorf("items path %q: key %q not found", path, key)
			}
			cur = next
		}
	}
	arr, ok := cur.([]any)
	if !ok {
		if path == "" {
			return nil, fmt.Errorf("command output is not a JSON array (set `items` if it is nested)")
		}
		return nil, fmt.Errorf("items path %q does not point to a JSON array", path)
	}
	return arr, nil
}

// renderItemTemplate substitutes "{field}" placeholders with the item's scalar values.
// A placeholder referencing an absent or non-scalar field is a LOUD error (a mapping
// bug must surface, not render blank).
func renderItemTemplate(tmpl string, item map[string]any) (string, error) {
	var rerr error
	out := placeholderRe.ReplaceAllStringFunc(tmpl, func(ph string) string {
		key := ph[1 : len(ph)-1]
		v, ok := item[key]
		if !ok {
			rerr = fmt.Errorf("template %q references field %q not present in item", tmpl, key)
			return ""
		}
		s, ok := jsonScalarString(v)
		if !ok {
			rerr = fmt.Errorf("template %q field %q is not a scalar", tmpl, key)
			return ""
		}
		return s
	})
	if rerr != nil {
		return "", rerr
	}
	return out, nil
}

// itemGroups extracts a string array from the named field. A missing/null field means
// no groups; a present-but-non-array (or non-string element) field is a loud error.
func itemGroups(item map[string]any, field string) ([]string, error) {
	v, ok := item[field]
	if !ok || v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("groups field %q is not an array", field)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("groups field %q has a non-string element", field)
		}
		out = append(out, s)
	}
	return out, nil
}

// jsonScalarString renders a JSON scalar (string/number/bool) as a string. JSON
// numbers decode as float64; an integral value prints without a trailing ".0".
func jsonScalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10), true
		}
		return strconv.FormatFloat(x, 'f', -1, 64), true
	default:
		return "", false
	}
}

func toAPIPlugin(s config.PluginSpec) api.PluginManifest {
	m := api.PluginManifest{Name: s.Name, Description: s.Description}
	for _, l := range s.Lists {
		m.Capabilities = append(m.Capabilities, api.PluginCapability{
			Name: l.Name, Kind: "list", Description: l.Description, Command: l.Command,
			Map: &api.PluginListMap{Items: l.Items, ID: l.ID, Label: l.Label, Groups: l.Groups, Path: l.Path},
		})
	}
	for _, a := range s.Actions {
		cap := api.PluginCapability{Name: a.Name, Kind: "action", Description: a.Description, Command: a.Command}
		for _, f := range a.Fields {
			cap.Fields = append(cap.Fields, api.PluginField{Name: f.Name, Label: f.Label, Type: f.Type, Required: f.Required})
		}
		m.Capabilities = append(m.Capabilities, cap)
	}
	return m
}
