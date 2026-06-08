package matrix

import "sort"

// MissingCells returns every expected cell (across all features) that has no
// bound test. TestMatrixComplete fails the build if this is non-empty: a
// missing test is a red build, never a silent blank.
func MissingCells() []Cell {
	var out []Cell
	for _, f := range Features() {
		for _, c := range f.ExpectedCells() {
			if !hasTest(c) {
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// OrphanCells returns every bound test whose cell is NOT an expected cell of any
// registered feature. RegisterTest already panics on this, so it should always
// be empty; the check exists as belt-and-suspenders against registry drift.
func OrphanCells() []Cell {
	var out []Cell
	for _, b := range boundCells() {
		f, ok := FeatureByID(b.cell.Feature)
		if !ok || !expected(f, b.cell) {
			out = append(out, b.cell)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}
