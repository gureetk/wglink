package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/nodetest"
)

// fakeLab makes an inventory whose nodes are fakes reached through exec.
func fakeLab(t *testing.T) (dir string, fakes map[string]*nodetest.Fake) {
	t.Helper()
	shell := nodetest.Shell(t)
	dir = t.TempDir()
	wg(t, dir, "init")
	fakes = map[string]*nodetest.Fake{}
	for i, name := range []string{"pve1", "nas", "pi"} {
		f := nodetest.New(t, shell, (i+1)*100)
		fakes[name] = f
		wg(t, dir, "node", "add", name, "--exec", strings.Join(f.Exec(), " "),
			"--endpoint", fmt.Sprintf("192.168.1.%d0", i+1), "--service", "systemd", "--no-check")
	}
	return dir, fakes
}

func code(err error) int {
	var ec *exitCode
	if errors.As(err, &ec) {
		return ec.code
	}
	if err != nil {
		return 1
	}
	return 0
}

func TestPlanAndApply(t *testing.T) {
	dir, fakes := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")

	out := wg(t, dir, "plan")
	for _, want := range []string{"network lab   wg-lab   10.99.0.0/24   udp/51820", "+ pve1  10.99.0.1", "2 to create", `Run "wglink apply"`} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCLI("--dir", dir, "plan", "--exit-code"); code(err) != 2 {
		t.Errorf("plan --exit-code = %v, want 2 with changes pending", err)
	}

	out = wg(t, dir, "apply", "--yes")
	for _, want := range []string{"pve1: created a key for wg-lab", "nas: created wg-lab", "Done."} {
		if !strings.Contains(out, want) {
			t.Errorf("apply lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(fakes["nas"].Dir, "wg-lab.conf")); err != nil {
		t.Errorf("nas has no config: %v", err)
	}
	if out := wg(t, dir, "plan"); !strings.Contains(out, "Everything is up to date.") {
		t.Errorf("plan after apply:\n%s", out)
	}
	if _, err := runCLI("--dir", dir, "plan", "--exit-code"); err != nil {
		t.Errorf("plan --exit-code = %v, want 0 when up to date", err)
	}
	if out := wg(t, dir, "net", "show", "lab"); !strings.Contains(out, "10.99.0.0/24 (picked)") || !strings.Contains(out, "nas      10.99.0.2") {
		t.Errorf("net show after apply:\n%s", out)
	}
}

func TestApplyConfirmation(t *testing.T) {
	dir, fakes := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")

	out, err := runCLIIn("n\n", "--dir", dir, "apply")
	if err != nil || !strings.Contains(out, "Nothing changed.") {
		t.Errorf("declined apply: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(fakes["pve1"].Dir, "wg-lab.conf")); !os.IsNotExist(err) {
		t.Error("declined apply changed a node")
	}
	out, err = runCLIIn("y\n", "--dir", dir, "apply")
	if err != nil || !strings.Contains(out, "Done.") {
		t.Errorf("confirmed apply: %v\n%s", err, out)
	}
}

func TestPlanProblemsExitOne(t *testing.T) {
	dir, fakes := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")
	os.WriteFile(filepath.Join(fakes["nas"].Dir, "wg-lab.conf"), []byte("[Interface]\n"), 0o600)

	out, err := runCLI("--dir", dir, "plan")
	if code(err) != 1 || !strings.Contains(out, "--adopt") {
		t.Errorf("plan: %v\n%s", err, out)
	}
	if _, err := runCLI("--dir", dir, "apply", "--yes"); code(err) != 1 {
		t.Errorf("apply with problems = %v", err)
	}
	if out := wg(t, dir, "apply", "--yes", "--adopt"); !strings.Contains(out, "Done.") {
		t.Errorf("apply --adopt:\n%s", out)
	}
}

func TestApplyScope(t *testing.T) {
	dir, fakes := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")
	wg(t, dir, "connect", "nas", "pi", "--name", "other")
	wg(t, dir, "apply", "--yes", "--net", "lab")
	if _, err := os.Stat(filepath.Join(fakes["pi"].Dir, "wg-other.conf")); !os.IsNotExist(err) {
		t.Error("--net lab touched network other")
	}
	if _, err := runCLI("--dir", dir, "plan", "--net", "ghost"); err == nil || !strings.Contains(err.Error(), "no network named ghost") {
		t.Errorf("unknown --net: %v", err)
	}
	fakes["pi"].Fail("ssh: connect to host pi port 22: Connection refused")
	out, err := runCLI("--dir", dir, "plan")
	if code(err) != 1 || !strings.Contains(out, "Connection refused") {
		t.Errorf("unreachable pi: %v\n%s", err, out)
	}
}
