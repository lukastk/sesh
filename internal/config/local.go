package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// config.local.toml is this machine's OVERLAY on config.toml. config.toml can
// then be one file shared by every machine (myrig symlinks it into a synced
// checkout) while what genuinely differs per machine lives in the local file.
// Every loader reads the MERGED document (readConfig), so a key behaves the same
// wherever it is written:
//
//   - a TABLE in the local file merges into the shared one key by key, at any
//     depth ([spawn.claude] model in the local file leaves the shared
//     [spawn.claude] args alone);
//   - ANY OTHER value — a string, a number, an array, an array of tables such as
//     [[hooks]] or [[cwd_label]] — REPLACES the shared value wholesale. A local
//     [[hooks]] list is the whole list on this machine, never appended to the
//     shared one, so there are no silent duplicates and no question of order.
//
// Either file may be missing. Both missing is the legitimate "no config" state.

// LocalConfigPath is <home>/config.local.toml.
func LocalConfigPath(home string) string { return filepath.Join(home, "config.local.toml") }

// ConfigDesc names the config file(s) a loader read, for error messages: the
// shared path, plus the local one when it exists.
func ConfigDesc(home string) string {
	if _, err := os.Stat(LocalConfigPath(home)); err == nil {
		return ConfigPath(home) + " + " + LocalConfigPath(home)
	}
	return ConfigPath(home)
}

// readConfig returns the merged config document as TOML bytes, or an error
// satisfying errors.Is(err, os.ErrNotExist) when neither file exists — exactly
// what the loaders' os.ReadFile used to return, so their missing-file handling
// is unchanged. A syntax error names the file it is in.
func readConfig(home string) ([]byte, error) {
	shared, sharedOK, err := readTOMLTable(ConfigPath(home))
	if err != nil {
		return nil, err
	}
	local, localOK, err := readTOMLTable(LocalConfigPath(home))
	if err != nil {
		return nil, err
	}
	switch {
	case !sharedOK && !localOK:
		return nil, fmt.Errorf("config: neither %s nor %s exists: %w", ConfigPath(home), LocalConfigPath(home), os.ErrNotExist)
	case !localOK:
		// The common case: no overlay. Return the file's own bytes, not a
		// re-encoding, so nothing about its decoding changes.
		return os.ReadFile(ConfigPath(home))
	case !sharedOK:
		return os.ReadFile(LocalConfigPath(home))
	}
	merged := mergeTables(shared, local)
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(merged); err != nil {
		return nil, fmt.Errorf("config: merge %s into %s: %w", LocalConfigPath(home), ConfigPath(home), err)
	}
	return buf.Bytes(), nil
}

// readTOMLTable parses one config file into a generic table. ok=false = absent.
func readTOMLTable(path string) (map[string]any, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("config: read %s: %w", path, err)
	}
	var doc map[string]any
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return nil, false, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, true, nil
}

// mergeTables overlays local onto shared: tables merge recursively, everything
// else is replaced. Neither input is modified.
func mergeTables(shared, local map[string]any) map[string]any {
	out := make(map[string]any, len(shared)+len(local))
	for k, v := range shared {
		out[k] = v
	}
	for k, lv := range local {
		lt, lIsTable := lv.(map[string]any)
		st, sIsTable := out[k].(map[string]any)
		if lIsTable && sIsTable {
			out[k] = mergeTables(st, lt)
			continue
		}
		out[k] = lv
	}
	return out
}

// localSetsSpawnModel reports whether config.local.toml sets agent's default
// model — which would shadow a value written to the shared config.toml.
func localSetsSpawnModel(home, agent string) (bool, error) {
	local, ok, err := readTOMLTable(LocalConfigPath(home))
	if err != nil || !ok {
		return false, err
	}
	spawn, _ := local["spawn"].(map[string]any)
	table, _ := spawn[agent].(map[string]any)
	_, set := table["model"]
	return set, nil
}
