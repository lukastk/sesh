package main

import "os/exec"

// execCapture runs a command and returns its combined output.
func execCapture(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, args...).CombinedOutput()
	return string(out), err
}
