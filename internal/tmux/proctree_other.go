//go:build !linux

package tmux

// NewProcSnapshotFor is the full `ps` snapshot on platforms without /proc
// (macOS): the process table is the only source of the subtree.
func NewProcSnapshotFor(panePIDs []int) (*ProcSnapshot, error) {
	return NewProcSnapshot()
}
