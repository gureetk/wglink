package cli

import "testing"

func TestUIChecks(t *testing.T) {
	dir := newInventory(t)
	wgErr(t, dir, "--every: check at most once a second", "ui", "--every", "100ms")
	wgErr(t, dir, "the dashboard needs a terminal", "ui")
}
