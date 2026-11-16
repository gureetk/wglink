package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/wgconf"
)

func TestExport(t *testing.T) {
	dir, _ := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")
	wg(t, dir, "apply", "--yes")

	out := wg(t, dir, "node", "add", "phone", "--manual", "--keepalive", "25")
	if !strings.Contains(out, `run "wglink export NETWORK phone"`) {
		t.Errorf("node add:\n%s", out)
	}
	if n := load(t, dir).Nodes["phone"]; n.Manual == nil || n.Manual.PubKey != "" || !n.Endpoint.None {
		t.Errorf("phone = %+v", n)
	}
	if out := wg(t, dir, "node", "list"); !regexp.MustCompile(`phone +manual \(no key yet\) +none`).MatchString(out) {
		t.Errorf("node list:\n%s", out)
	}
	wg(t, dir, "net", "join", "lab", "phone")

	file := filepath.Join(t.TempDir(), "phone.conf")
	os.WriteFile(file, []byte("old"), 0o644)
	wg(t, dir, "export", "lab", "phone", "-o", file)
	info, _ := os.Stat(file)
	conf, _ := os.ReadFile(file)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	m := regexp.MustCompile(`(?m)^PrivateKey = (\S+)$`).FindStringSubmatch(string(conf))
	if m == nil || !wgconf.ValidKey(m[1]) || !strings.Contains(string(conf), "Address = 10.99.0.3/24") || !strings.Contains(string(conf), "# nas\n") || !strings.Contains(string(conf), "# pve1\n") {
		t.Fatalf("config:\n%s", conf)
	}
	pub, _ := wgconf.PublicKey(m[1])
	if n := load(t, dir).Nodes["phone"]; n.Manual.PubKey != pub || n.Keepalive != 25 || !n.Endpoint.None {
		t.Errorf("phone after export = %+v, want pubkey %s", n, pub)
	}

	plan := wg(t, dir, "plan")
	if !strings.Contains(plan, "peer phone") || !strings.Contains(plan, "(live)") || strings.Contains(plan, "to export") {
		t.Errorf("plan after export:\n%s", plan)
	}
	wg(t, dir, "apply", "--yes")
	if plan := wg(t, dir, "plan"); !strings.Contains(plan, "Everything is up to date.") {
		t.Errorf("plan after apply:\n%s", plan)
	}

	again := wg(t, dir, "export", "lab", "phone")
	if !strings.Contains(again, "PrivateKey = (the private key from phone's first export)") || load(t, dir).Nodes["phone"].Manual.PubKey != pub {
		t.Errorf("second export:\n%s", again)
	}
	fresh := wg(t, dir, "export", "lab", "phone", "--new-key")
	if strings.Contains(fresh, m[1]) || load(t, dir).Nodes["phone"].Manual.PubKey == pub {
		t.Error("--new-key kept the old key")
	}

	wgErr(t, dir, "pve1 isn't a manual node", "export", "lab", "pve1")
	wgErr(t, dir, "no node named ghost", "export", "lab", "ghost")
	wgErr(t, dir, "isn't a WireGuard public key", "node", "add", "tab", "--manual", "--pubkey", "nope")
	wgErr(t, dir, "--pubkey goes with --manual", "node", "add", "tab", "--ssh", "tab", "--pubkey", pub, "--no-check")
	wg(t, dir, "node", "add", "tab", "--manual")
	wgErr(t, dir, "tab isn't in lab", "export", "lab", "tab")
}
