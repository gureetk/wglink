package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/config"
)

// wg runs wglink against dir and fails the test on error.
func wg(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runCLI(append([]string{"--dir", dir}, args...)...)
	if err != nil {
		t.Fatalf("wglink %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// wgErr runs wglink against dir and expects an error containing want.
func wgErr(t *testing.T, dir, want string, args ...string) {
	t.Helper()
	_, err := runCLI(append([]string{"--dir", dir}, args...)...)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("wglink %s: err = %v, want %q", strings.Join(args, " "), err, want)
	}
}

func load(t *testing.T, dir string) *config.Inventory {
	t.Helper()
	inv, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

// newInventory makes a directory with wglink.yaml and a few nodes.
func newInventory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wg(t, dir, "init")
	wg(t, dir, "node", "add", "pve1", "--ssh", "root@192.168.1.10", "--no-check")
	wg(t, dir, "node", "add", "nas", "--ssh", "admin@nas.lan", "--sudo", "--endpoint", "192.168.1.20", "--no-check")
	wg(t, dir, "node", "add", "ct101", "--pct", "101", "--via", "pve1", "--endpoint", "192.168.1.101", "--no-check")
	return dir
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	out := wg(t, dir, "init", "--pool", "10.50.0.0/16")
	if !strings.Contains(out, "Created") {
		t.Errorf("output: %s", out)
	}
	inv := load(t, dir)
	if inv.Defaults.Pool.String() != "10.50.0.0/16" {
		t.Errorf("pool = %s", inv.Defaults.Pool)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); !strings.Contains(string(b), ".wglink.state.json.lock") {
		t.Errorf(".gitignore = %q", b)
	}
	wgErr(t, dir, "already exists", "init")
	wgErr(t, t.TempDir(), "--pool", "init", "--pool", "nonsense")
}

func TestNodeCommands(t *testing.T) {
	dir := newInventory(t)
	wg(t, dir, "node", "add", "web", "--exec", "docker exec -i web", "--endpoint", "none", "--no-check")
	wg(t, dir, "node", "add", "laptop", "--local", "--endpoint", "192.168.1.50", "--keepalive", "25", "--service", "none", "--no-check")
	inv := load(t, dir)
	if n := inv.Nodes["nas"]; n.SSH != "admin@nas.lan" || !n.Sudo || n.Endpoint.Host != "192.168.1.20" {
		t.Errorf("nas = %+v", n)
	}
	if n := inv.Nodes["ct101"]; n.PCT == nil || n.PCT.Via != "pve1" || n.PCT.VMID != 101 {
		t.Errorf("ct101 = %+v", n)
	}
	if n := inv.Nodes["web"]; strings.Join(n.Exec, " ") != "docker exec -i web" || !n.Endpoint.None {
		t.Errorf("web = %+v", n)
	}
	if n := inv.Nodes["laptop"]; !n.Local || n.Keepalive != 25 || n.Service != "none" {
		t.Errorf("laptop = %+v", n)
	}

	list := wg(t, dir, "node", "list")
	for _, want := range []string{"NAME", "ct101    pct 101 via pve1", "ssh admin@nas.lan (sudo)", "exec docker exec -i web", "ssh host"} {
		if !strings.Contains(list, want) {
			t.Errorf("node list lacks %q:\n%s", want, list)
		}
	}

	wgErr(t, dir, "already exists", "node", "add", "nas", "--local", "--no-check")
	wgErr(t, dir, "use lowercase", "node", "add", "Bad_Name", "--local", "--no-check")
	wgErr(t, dir, "say how to reach it", "node", "add", "x", "--no-check")
	wgErr(t, dir, "--pct needs --via", "node", "add", "x", "--pct", "102", "--no-check")
	wgErr(t, dir, "none of the others can be", "node", "add", "x", "--ssh", "a", "--local", "--no-check")
	wgErr(t, dir, "must not start with -", "node", "add", "x", "--ssh", "-oProxyCommand=evil", "--no-check")
	if _, ok := load(t, dir).Nodes["x"]; ok {
		t.Error("an invalid node reached wglink.yaml")
	}

	wgErr(t, dir, "ct101 is reached through pve1", "node", "rm", "pve1")
	wg(t, dir, "connect", "nas", "laptop")
	wgErr(t, dir, "use --force", "node", "rm", "nas")
	wg(t, dir, "node", "rm", "nas", "--force")
	inv = load(t, dir)
	if _, ok := inv.Nodes["nas"]; ok {
		t.Error("nas still present")
	}
	if m := inv.Networks["nas-laptop"].Members; len(m) != 1 || m[0].Name != "laptop" {
		t.Errorf("members = %+v", m)
	}
	wgErr(t, dir, "no node named", "node", "rm", "ghost")
}

func TestConnect(t *testing.T) {
	dir := newInventory(t)
	out := wg(t, dir, "connect", "pve1", "nas")
	if !strings.Contains(out, "Network pve1-nas: pve1, nas.") {
		t.Errorf("output: %s", out)
	}
	wg(t, dir, "connect", "pve1", "nas", "ct101")
	wg(t, dir, "connect", "nas", "pve1", "--name", "backup", "--subnet", "172.30.5.0/24", "--port", "51900", "--ip", "nas=.10")
	inv := load(t, dir)
	if _, ok := inv.Networks["net1"]; !ok {
		t.Errorf("three nodes should make net1: %v", inv.NetworkNames())
	}
	b := inv.Networks["backup"]
	if b.Subnet.String() != "172.30.5.0/24" || b.Port != 51900 {
		t.Errorf("backup = %+v", b)
	}
	if m, _ := b.Member("nas"); m.Addr.String() != ".10" {
		t.Errorf("nas = %+v", m)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, config.FileName))
	if !strings.Contains(string(raw), `nas: ".10"`) {
		t.Errorf("pin should be quoted:\n%s", raw)
	}
	// Connecting more nodes to an existing network adds them.
	wg(t, dir, "connect", "ct101", "pve1", "--name", "backup")
	if n := len(load(t, dir).Networks["backup"].Members); n != 3 {
		t.Errorf("backup has %d members", n)
	}
	wgErr(t, dir, "no node named ghost", "connect", "pve1", "ghost")
	wgErr(t, dir, "isn't one of the nodes", "connect", "pve1", "nas", "--ip", "ct101=.5")
	wgErr(t, dir, "want NODE=ADDRESS", "connect", "pve1", "nas", "--name", "x", "--ip", "nas")
	wgErr(t, dir, "requires at least 2", "connect", "pve1")
}

func TestNetCommands(t *testing.T) {
	dir := newInventory(t)
	wg(t, dir, "net", "create", "lab", "--mtu", "1380")
	wg(t, dir, "net", "join", "lab", "pve1", "nas")
	wg(t, dir, "net", "join", "lab", "ct101", "--ip", ".50")
	inv := load(t, dir)
	lab := inv.Networks["lab"]
	if lab.MTU != 1380 || len(lab.Members) != 3 {
		t.Errorf("lab = %+v", lab)
	}
	if m, _ := lab.Member("ct101"); m.Addr.String() != ".50" {
		t.Errorf("ct101 = %+v", m)
	}

	wgErr(t, dir, "needs a fixed subnet", "ip", "lab", "nas", "10.99.9.9")
	wg(t, dir, "net", "set", "lab", "--subnet", "10.99.9.0/24")
	wg(t, dir, "ip", "lab", "nas", "10.99.9.9")
	if m, _ := load(t, dir).Networks["lab"].Member("nas"); m.Addr.String() != "10.99.9.9" {
		t.Errorf("nas = %+v", m)
	}
	wg(t, dir, "ip", "lab", "nas", "auto")
	wgErr(t, dir, "isn't in lab", "ip", "lab", "laptop", ".5")

	wg(t, dir, "net", "set", "lab", "--subnet", "auto", "--mtu", "auto", "--port", "51830", "--iface", "wglab")
	lab = load(t, dir).Networks["lab"]
	if !lab.Subnet.Auto() || lab.MTU != 0 || lab.Port != 51830 || lab.IfaceName() != "wglab" {
		t.Errorf("lab = %+v", lab)
	}
	wgErr(t, dir, "nothing to change", "net", "set", "lab")
	wgErr(t, dir, "--mtu", "net", "set", "lab", "--mtu", "99")

	list := wg(t, dir, "net", "list")
	if !strings.Contains(list, "lab    wglab   auto     51830") {
		t.Errorf("net list:\n%s", list)
	}
	show := wg(t, dir, "net", "show", "lab")
	for _, want := range []string{"interface  wglab", "subnet     auto (picked at the next apply)", "port       51830 (pinned)", "ct101    -         pinned .50"} {
		if !strings.Contains(show, want) {
			t.Errorf("net show lacks %q:\n%s", want, show)
		}
	}

	wg(t, dir, "net", "leave", "lab", "nas")
	wgErr(t, dir, "isn't in lab", "net", "leave", "lab", "nas")
	wg(t, dir, "net", "rm", "lab")
	if len(load(t, dir).Networks) != 0 {
		t.Error("lab still present")
	}
	wgErr(t, dir, "no network named", "net", "rm", "lab")
	wgErr(t, dir, "at most 12", "net", "create", "averyverylongname")
}

func TestNoInventory(t *testing.T) {
	wgErr(t, t.TempDir(), "run \"wglink init\"", "node", "list")
}

func TestEmptyLists(t *testing.T) {
	dir := t.TempDir()
	wg(t, dir, "init")
	if out := wg(t, dir, "node", "list"); !strings.Contains(out, "No nodes yet") {
		t.Errorf("node list: %s", out)
	}
	if out := wg(t, dir, "net", "list"); !strings.Contains(out, "No networks yet") {
		t.Errorf("net list: %s", out)
	}
}
