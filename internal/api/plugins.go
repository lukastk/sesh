package api

// Plugins are the daemon's "command-provider" substrate: a manifest at
// <SESH_HOME>/plugins/*.toml declares commands the daemon runs ON ITS OWN HOST and
// how the sesh-ui app surfaces them. Two capability kinds:
//
//   - "list": runs a command whose JSON output is MAPPED to []{id,label,groups,path}
//     (e.g. `boxyard list --output-format json` → the new-thread cwd picker, WITH
//     box groups).
//   - "action": runs a command with form FIELD values substituted as ARGV (e.g.
//     `boxyard new --name {name}` → create-a-box from the app).
//
// The app (especially mobile / a remote daemon) has no shell on the target machine,
// so these machine ops MUST go via the daemon. The endpoints are reachable
// cross-machine over the peer's transport, exactly like fs/list, so the app can drive
// the plugins on whichever machine it is deploying to. Field values are passed as
// ARGV (never a shell string → no injection); the command itself comes from the
// manifest ONLY, never the client.

// PluginField is one input a "action" capability renders as a form control.
type PluginField struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`               // text | number | boolean
	Required bool   `json:"required,omitempty"` // UI hint; a missing required field is a loud 400
}

// PluginListMap describes how a "list" capability's JSON output is turned into
// []PluginListItem. id/label/path are TEMPLATES over each item's fields ("{field}"
// placeholders, like config.toml's cwd_label); groups names a field that must hold a
// string array. Items is a dotted path to the array within the output (empty = the
// output's root is the array).
type PluginListMap struct {
	Items  string `json:"items,omitempty"`
	ID     string `json:"id"`
	Label  string `json:"label"`
	Groups string `json:"groups,omitempty"`
	Path   string `json:"path,omitempty"`
}

// PluginCapability is one named capability of a plugin (kind "list" or "action").
// Command is the daemon-side argv it runs; it is shown for transparency but is never
// taken from the client.
type PluginCapability struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"` // "list" | "action"
	Description string         `json:"description,omitempty"`
	Command     []string       `json:"command"`
	Fields      []PluginField  `json:"fields,omitempty"` // kind=action
	Map         *PluginListMap `json:"map,omitempty"`    // kind=list
}

// PluginManifest is one <SESH_HOME>/plugins/*.toml file, flattened to a single
// capabilities list (the TOML's [[list]] and [[action]] tables) for the generic
// POST /v1/plugins/{name}/{capability} endpoint.
type PluginManifest struct {
	Name         string             `json:"name"`
	Description  string             `json:"description,omitempty"`
	Capabilities []PluginCapability `json:"capabilities"`
}

// PluginsListResponse is GET /v1/plugins — every manifest on this daemon's host.
type PluginsListResponse struct {
	Schema  int              `json:"schema"`
	Plugins []PluginManifest `json:"plugins"`
}

// PluginRunRequest is the POST /v1/plugins/{name}/{capability} body: the form field
// values for an "action" capability (ignored for "list", which takes no fields).
type PluginRunRequest struct {
	Fields map[string]string `json:"fields,omitempty"`
}

// PluginListItem is one mapped entry of a "list" capability's output.
type PluginListItem struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Groups []string `json:"groups,omitempty"`
	Path   string   `json:"path,omitempty"`
}

// PluginRunResponse is the result of running a capability. For "list" Items is the
// mapped output; for "action" Output is the command's stdout. A nonzero exit is NOT a
// success — it is returned as a loud HTTP error, never an empty-but-200 body.
type PluginRunResponse struct {
	Schema     int              `json:"schema"`
	Plugin     string           `json:"plugin"`
	Capability string           `json:"capability"`
	Kind       string           `json:"kind"`
	Items      []PluginListItem `json:"items,omitempty"`
	Output     string           `json:"output,omitempty"`
}
