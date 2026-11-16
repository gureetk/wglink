package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/node"
)

func TestStatus(t *testing.T) {
	dir, fakes := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")
	wg(t, dir, "apply", "--yes")
	for _, name := range []string{"pve1", "nas"} {
		fakes[name].WriteStub("now", "1700000100")
		fakes[name].WriteStub("handshake", "1700000090")
	}

	out := wg(t, dir, "status", "--no-ping")
	for _, want := range []string{"network lab   wg-lab   10.99.0.0/24   udp/51820", "pve1 <-> nas  ok  handshake 10s ago", "The link is ok."} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCLI("--dir", dir, "status", "--no-ping", "--exit-code"); err != nil {
		t.Errorf("status --exit-code = %v, want 0 when every link is ok", err)
	}

	var got statusOut
	if err := json.Unmarshal([]byte(wg(t, dir, "status", "--json", "--no-ping")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Networks) != 1 || len(got.Networks[0].Links) != 1 || len(got.Networks[0].Members) != 2 {
		t.Fatalf("json = %+v", got)
	}
	l := got.Networks[0].Links[0]
	if l.A != "pve1" || l.B != "nas" || l.State != "ok" || l.HandshakeAge == nil || *l.HandshakeAge != 10 || l.RTT != nil || l.RX != 1000 {
		t.Errorf("link = %+v", l)
	}
	if m := got.Networks[0].Members[1]; m.Node != "nas" || m.Address != "10.99.0.2" || m.State != "up" || m.Drift {
		t.Errorf("member = %+v", m)
	}

	// No handshakes and no ping replies: the link is down.
	fakes["pve1"].WriteStub("handshake", "0")
	fakes["nas"].WriteStub("handshake", "0")
	out, err := runCLI("--dir", dir, "status", "--exit-code")
	if code(err) != 2 || !strings.Contains(out, "pve1 <-> nas  down  no handshake yet, no ping reply") {
		t.Errorf("status --exit-code = %v\n%s", err, out)
	}

	fakes["nas"].Fail("ssh: connect to host nas port 22: Connection refused")
	out, _ = runCLI("--dir", dir, "status", "--no-ping")
	if !strings.Contains(out, "nas   10.99.0.2       unreachable") || !strings.Contains(out, "port 22: Connection refused") || !strings.Contains(out, "Couldn't reach nas.") {
		t.Errorf("status with nas down:\n%s", out)
	}
	wgErr(t, dir, "no network named ghost", "status", "--net", "ghost")
}

func TestStatusLocal(t *testing.T) {
	dir, fakes := fakeLab(t)
	wg(t, dir, "connect", "pve1", "nas", "--name", "lab")
	wg(t, dir, "apply", "--yes")
	f := fakes["pve1"]
	f.WriteStub("now", "1700000100")
	f.WriteStub("handshake", "1700000097")
	saved := localNode
	t.Cleanup(func() { localNode = saved })
	localNode = node.Node{Runner: f.Runner(), Dir: f.Dir}

	out := wg(t, dir, "status", "--local", "--no-ping")
	if !strings.Contains(out, "interface wg-lab   network lab   10.99.0.1/24   udp/51820\n  nas  ok  handshake 3s ago") {
		t.Errorf("status --local:\n%s", out)
	}

	// With no wglink.yaml to find, status looks at this machine.
	t.Setenv("WGLINK_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	out, err := runCLI("status", "--json", "--no-ping")
	if err != nil {
		t.Fatal(err)
	}
	var got localOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(got.Interfaces) != 1 || got.Interfaces[0].Network != "lab" || len(got.Interfaces[0].Peers) != 1 {
		t.Fatalf("json = %+v", got)
	}
	if p := got.Interfaces[0].Peers[0]; p.Name != "nas" || p.Address != "10.99.0.2" || p.State != "ok" || *p.HandshakeAge != 3 {
		t.Errorf("peer = %+v", p)
	}
	if _, err := runCLI("status", "--net", "lab"); err == nil || !strings.Contains(err.Error(), "no wglink.yaml") {
		t.Errorf("--net without an inventory: %v", err)
	}
}
