package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lukastk/sesh/internal/config"
)

// runMesh prints the merged cross-machine view from the local daemon's cache
// (GET /v1/mesh) — every machine's threads with their live state and how fresh the
// data is. A local read; offline machines show their last-known threads as stale.
func runMesh(args []string) error {
	fs := flag.NewFlagSet("mesh", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the full MeshSnapshot as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := daemonClient(config.Load())
	mesh, err := c.Mesh(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(mesh)
	}
	now := time.Now().Unix()
	for _, mv := range mesh.Machines {
		fresh := "self"
		if !mv.Self {
			age := now - mv.SyncedAtUnix
			if mv.Reachable {
				fresh = fmt.Sprintf("synced %ds ago", age)
			} else {
				fresh = fmt.Sprintf("OFFLINE — last seen %ds ago", age)
			}
		}
		fmt.Printf("== %s (%s) — %d threads ==\n", mv.Machine, fresh, len(mv.Threads))
		for _, t := range mv.Threads {
			fmt.Printf("  %-7s %-8s %-7s %s\n", t.Activity, t.Attachment, t.AgentKind, t.Name)
		}
	}
	return nil
}
