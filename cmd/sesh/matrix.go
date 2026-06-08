package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/lukastk/sesh/internal/matrix"
)

// runMatrix implements `sesh matrix <grid|skips>`. It reports the state of the
// last test run from the persisted artifact (written by `go test
// ./internal/conformance`). It does NOT itself run tests — the matrix is a
// measurement of the conformance suite, kept honestly separate from it.
func runMatrix(args []string) error {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	artifact := fs.String("artifact", matrix.ArtifactPath(), "path to the run artifact")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sub := "grid"
	if fs.NArg() > 0 {
		sub = fs.Arg(0)
	}

	snap, err := matrix.ReadArtifact(*artifact)
	if err != nil {
		return fmt.Errorf("read artifact %s: %w (run `go test ./internal/conformance` first)", *artifact, err)
	}

	switch sub {
	case "grid":
		if *asJSON {
			return emitJSON(snap)
		}
		matrix.RenderSnapshotGrid(os.Stdout, snap)
		if !matrix.TallySnapshot(snap).AllGreen() {
			os.Exit(1)
		}
		return nil
	case "skips":
		skips := matrix.SnapshotSkips(snap)
		if *asJSON {
			return emitJSON(skips)
		}
		if len(skips) == 0 {
			fmt.Println("no skipped cells")
			return nil
		}
		for _, sk := range skips {
			fmt.Printf("%s\t%s\n", sk.Cell.ID(), sk.Reason)
		}
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (want: grid | skips)", sub)
	}
}

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
