package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestStateRebuild(t *testing.T) {
	dir, _ := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")
	wg(t, dir, "apply", "--yes")
	before, _ := os.ReadFile(filepath.Join(dir, "wglink.state.json"))

	out := wg(t, dir, "state", "rebuild")
	for _, want := range []string{
		`network lab   wg-lab   10\.99\.0\.0/24   udp/51820`,
		`  pve1  10\.99\.0\.1  its config`,
		`  nas   10\.99\.0\.2  its config`,
		`Saved wglink\.state\.json \(the old one is wglink\.state\.json\.bak\)`,
	} {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("rebuild lacks %s:\n%s", want, out)
		}
	}
	if bak, _ := os.ReadFile(filepath.Join(dir, "wglink.state.json.bak")); string(bak) != string(before) {
		t.Error("the old state wasn't kept")
	}
	if out := wg(t, dir, "plan"); !strings.Contains(out, "Everything is up to date.") {
		t.Errorf("plan after rebuild:\n%s", out)
	}

	os.Remove(filepath.Join(dir, "wglink.state.json"))
	out = wg(t, dir, "state", "rebuild", "--net", "lab")
	if !strings.Contains(out, "Saved wglink.state.json. Run") {
		t.Errorf("rebuild without a state file:\n%s", out)
	}
	wgErr(t, dir, "no network named ghost", "state", "rebuild", "--net", "ghost")
}
