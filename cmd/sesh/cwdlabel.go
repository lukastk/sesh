package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/lukastk/sesh/internal/config"
)

// runCwdLabel applies the [[cwd_label]] rules to a path and prints the label —
// the SAME in-process transform the TUI's CWD column uses, exposed so scripts
// and hooks render cwds identically. No rule matched → the ~-relative path.
// Pure config transform; no daemon needed. A malformed rule is a loud error.
func runCwdLabel(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sesh cwd-label <path>")
	}
	cfg := config.Load()
	labels, err := config.LoadCwdLabels(cfg.Home)
	if err != nil {
		return err
	}
	uh, _ := os.UserHomeDir()
	out, err := labels.LabelFor(args[0], uh)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
