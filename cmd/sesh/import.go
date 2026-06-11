package main

// `sesh import --from-v1 <v1-SESH_HOME>` (PARITY_ROADMAP E4): bring v1 sesh's
// thread records into v2. Per-machine (only THIS machine's v1 sessions);
// transcripts stay where the agents keep them (v2 resume works off the
// session id, which v1 and v2 share). --dry-run previews; existing threads
// (by id) are skipped, never overwritten; a loud per-record report.

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/migrate"
)

func runImport(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fromV1 := fs.String("from-v1", "", "the v1 SESH_HOME to import from (e.g. ~/.sesh) (required)")
	dryRun := fs.Bool("dry-run", false, "preview without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fromV1 == "" {
		return errors.New("import: --from-v1 <v1-home> is required")
	}
	sessions, err := migrate.ReadV1Sessions(*fromV1, cfg.Machine)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Printf("no v1 sessions for machine %q in %s\n", cfg.Machine, *fromV1)
		return nil
	}

	c := daemonClient(cfg)
	ctx := context.Background()
	imported, skipped := 0, 0
	for _, s := range sessions {
		th := s.ToThread()
		// Skip an already-present thread (idempotent re-import).
		if existing, ok := lookupThread(c, th.ID); ok {
			fmt.Printf("skip   %s  %-20s (already present as %q)\n", th.ID[:8], th.Name, existing.Name)
			skipped++
			continue
		}
		if *dryRun {
			fmt.Printf("would  %s  %-20s  %s  %s\n", th.ID[:8], th.Name, th.AgentKind, th.Cwd)
			imported++
			continue
		}
		if err := c.ThreadImport(ctx, th); err != nil {
			fmt.Printf("FAIL   %s  %-20s  %v\n", th.ID[:8], th.Name, err)
			continue
		}
		fmt.Printf("import %s  %-20s  %s\n", th.ID[:8], th.Name, th.AgentKind)
		imported++
	}
	verb := "imported"
	if *dryRun {
		verb = "would import"
	}
	fmt.Printf("\n%s %d, skipped %d (of %d v1 sessions)\n", verb, imported, skipped, len(sessions))
	return nil
}
