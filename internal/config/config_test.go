package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const example = `version: 1

defaults:
  pool: 10.99.0.0/16
  prefix: 24
  port: 51820

nodes:
  pve1:
    ssh: root@192.168.1.10
  nas:
    ssh: admin@nas.lan
    sudo: true
    endpoint: 192.168.1.20
  pi:
    ssh: pi-garage
  ct101:
    pct: { via: pve1, vmid: 101 }
    endpoint: 192.168.1.101
  vm200:
    qm: { via: pve1, vmid: 200 }
    endpoint: none
  phone:
    manual: { pubkey: yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk= }
    endpoint: none
    keepalive: 25
  tablet:
    manual: {}
    endpoint: none
  web:
    exec: [docker, exec, -i, web]
    endpoint: none
  laptop:
    local: true
    endpoint: 192.168.1.50

networks:
  lab:
    members: [pve1, nas, pi, ct101]
  backup:
    subnet: 172.30.5.0/24
    port: 51900
    members:
      nas: ".10"
      pve1: 172.30.5.20
      ct101: auto
      laptop: { ip: .30, routes: [192.168.60.0/24] }
`

func TestParseExample(t *testing.T) {
	inv, err := Parse("wglink.yaml", []byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(inv.NodeNames(), ","); got != "ct101,laptop,nas,phone,pi,pve1,tablet,vm200,web" {
		t.Errorf("nodes = %s", got)
	}
	if r := inv.Nodes["ct101"].Reach(); r != ReachPCT {
		t.Errorf("ct101 reach = %q", r)
	}
	if n := inv.Nodes["vm200"]; n.Reach() != ReachQM || n.QM.Via != "pve1" || n.QM.VMID != 200 {
		t.Errorf("vm200 = %+v", n)
	}
	if m, _ := inv.Networks["backup"].Member("laptop"); len(m.Routes) != 1 || m.Routes[0].String() != "192.168.60.0/24" || m.Addr.String() != ".30" {
		t.Errorf("laptop in backup = %+v", m)
	}
	if n := inv.Nodes["phone"]; n.Reach() != ReachManual || n.Manual.PubKey != "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=" {
		t.Errorf("phone = %+v", n)
	}
	if n := inv.Nodes["tablet"]; n.Reach() != ReachManual || n.Manual.PubKey != "" {
		t.Errorf("tablet = %+v", n)
	}
	if r := inv.Nodes["web"].Reach(); r != ReachExec {
		t.Errorf("web reach = %q", r)
	}
	if !inv.Nodes["web"].Endpoint.None {
		t.Error("web endpoint should be none")
	}
	lab := inv.Networks["lab"]
	if lab.IfaceName() != "wg-lab" || !lab.Subnet.Auto() || lab.Port != 0 {
		t.Errorf("lab = %+v", lab)
	}
	var names []string
	for _, m := range lab.Members {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "pve1,nas,pi,ct101" {
		t.Errorf("lab members = %v (want file order)", names)
	}
	backup := inv.Networks["backup"]
	want := map[string]string{"nas": ".10", "pve1": "172.30.5.20", "ct101": "auto", "laptop": ".30"}
	for _, m := range backup.Members {
		if m.Addr.String() != want[m.Name] {
			t.Errorf("backup member %s = %s, want %s", m.Name, m.Addr, want[m.Name])
		}
	}
	if got := strings.Join(inv.NetworksOf("nas"), ","); got != "backup,lab" {
		t.Errorf("NetworksOf(nas) = %s", got)
	}
}

func TestParseDefaults(t *testing.T) {
	inv, err := Parse("x", []byte("version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := inv.Defaults
	if d.Pool != DefaultPool || d.Prefix != DefaultPrefix || d.Port != DefaultPort {
		t.Errorf("defaults = %+v", d)
	}
	if inv.Nodes == nil || inv.Networks == nil {
		t.Error("maps should be non-nil")
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"empty", "", "version: want 1"},
		{"typo", "version: 1\nnodes:\n  a:\n    ssh: a\n    endpont: 1.2.3.4\n", `x:5: unknown field "endpont" in node (did you mean "endpoint"?)`},
		{"duplicate", "version: 1\nnodes:\n  a: {ssh: a}\n  a: {ssh: b}\n", `already defined`},
		{"no reach", "version: 1\nnodes:\n  a: {sudo: true}\n", "node a: set exactly one of"},
		{"two reaches", "version: 1\nnodes:\n  a: {ssh: a, local: true}\n", "node a: set exactly one of"},
		{"bad node name", "version: 1\nnodes:\n  Bad_Name: {ssh: a}\n", "node Bad_Name: name must be"},
		{"ssh option", "version: 1\nnodes:\n  a: {ssh: -oProxyCommand=x}\n", "must not start with -"},
		{"ssh space", "version: 1\nnodes:\n  a: {ssh: 'a b'}\n", "contains spaces"},
		{"empty exec", "version: 1\nnodes:\n  a: {exec: []}\n", "exec: give a command"},
		{"pct no host", "version: 1\nnodes:\n  c: {pct: {via: h, vmid: 101}, endpoint: none}\n", `pct.via: no node named "h"`},
		{"pct bad vmid", "version: 1\nnodes:\n  h: {ssh: h}\n  c: {pct: {via: h, vmid: 7}}\n", "pct.vmid: 7 must be"},
		{"pct via pct", "version: 1\nnodes:\n  h: {ssh: h}\n  c: {pct: {via: h, vmid: 101}}\n  d: {pct: {via: c, vmid: 102}}\n", "pct.via: c must be"},
		{"qm via qm", "version: 1\nnodes:\n  h: {ssh: h}\n  v: {qm: {via: h, vmid: 101}}\n  w: {qm: {via: v, vmid: 102}}\n", "qm.via: v must be"},
		{"pct and qm", "version: 1\nnodes:\n  h: {ssh: h}\n  v: {qm: {via: h, vmid: 101}, pct: {via: h, vmid: 101}}\n", "set exactly one of ssh, local, exec, pct, qm or manual"},
		{"manual key", "version: 1\nnodes:\n  p: {manual: {pubkey: nope}}\n", `manual.pubkey: "nope" isn't a WireGuard public key`},
		{"manual typo", "version: 1\nnodes:\n  p:\n    manual: {pubky: x}\n", `unknown field "pubky" in manual (did you mean "pubkey"?)`},
		{"manual endpoint", "version: 1\nnodes:\n  a: {ssh: a}\n  p: {manual: {}}\nnetworks:\n  n: {members: [a, p]}\n", "node p: endpoint: required for manual nodes"},
		{"via manual", "version: 1\nnodes:\n  p: {manual: {}}\n  c: {pct: {via: p, vmid: 101}}\n", "pct.via: p must be"},
		{"route not masked", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {members: {a: {routes: [192.168.50.1/24]}}}\n", "routes: write 192.168.50.1/24 as 192.168.50.0/24"},
		{"route default", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {members: {a: {routes: [0.0.0.0/0]}}}\n", "would send all traffic through a"},
		{"route in subnet", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {subnet: 10.9.0.0/24, members: {a: {routes: [10.9.0.0/25]}}}\n", "overlaps the network's subnet"},
		{"routes overlap", "version: 1\nnodes:\n  a: {ssh: a}\n  b: {ssh: b}\nnetworks:\n  n: {members: {a: {routes: [192.168.0.0/16]}, b: {routes: [192.168.50.0/24]}}}\n", "192.168.50.0/24 overlaps 192.168.0.0/16, which a routes"},
		{"ipv6 v4", "version: 1\nnetworks:\n  n: {ipv6: 10.9.0.0/24, members: []}\n", "must be an IPv6 subnet"},
		{"ipv6 host bits", "version: 1\nnetworks:\n  n: {ipv6: 'fd00::1/64', members: []}\n", "did you mean fd00::/64?"},
		{"ipv6 route", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {ipv6: 'fd00:1::/64', members: {a: {routes: ['fd00:1::/80']}}}\n", "overlaps the network's IPv6 subnet"},
		{"routes typo", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {members: {a: {route: [192.168.0.0/16]}}}\n", `unknown field "route" in member (did you mean "routes"?)`},
		{"sudo exec", "version: 1\nnodes:\n  a: {exec: [x], sudo: true}\n", "sudo only applies"},
		{"service", "version: 1\nnodes:\n  a: {ssh: a, service: openrc}\n", "service:"},
		{"endpoint needed", "version: 1\nnodes:\n  a: {ssh: a}\n  b: {local: true}\nnetworks:\n  n: {members: [a, b]}\n", "node b: endpoint: required"},
		{"bad endpoint", "version: 1\nnodes:\n  a: {ssh: a, endpoint: 'x:0'}\n", "x:3: endpoint"},
		{"unknown member", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {members: [a, ghost]}\n", "member ghost: no node"},
		{"member twice", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {members: [a, a]}\n", "member a: listed twice"},
		{"long net name", "version: 1\nnetworks:\n  thirteenchars: {}\n", "at most 12 long"},
		{"iface clash", "version: 1\nnetworks:\n  a: {iface: wg0}\n  b: {iface: wg0}\n", "interface wg0 is also used"},
		{"bad subnet", "version: 1\nnetworks:\n  n: {subnet: 10.0.0.1/24}\n", "x:3: 10.0.0.1/24 has host bits set"},
		{"pin outside", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n:\n    subnet: 10.1.0.0/24\n    members: {a: 10.2.0.1}\n", "outside subnet"},
		{"full addr auto subnet", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n: {members: {a: 10.99.0.9}}\n", "needs a fixed subnet"},
		{"pin clash", "version: 1\nnodes:\n  a: {ssh: a}\n  b: {ssh: b}\nnetworks:\n  n:\n    subnet: 10.1.0.0/24\n    members: {a: .5, b: 10.1.0.5}\n", "also pinned for a"},
		{"bad spec", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n:\n    members:\n      a: .999\n", "x:7: member a: address"},
		{"member typo", "version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n:\n    members:\n      a: {iq: .5}\n", `unknown field "iq" in member (did you mean "ip"?)`},
		{"mtu", "version: 1\nnetworks:\n  n: {mtu: 100}\n", "mtu: 100"},
		{"pool", "version: 1\ndefaults: {pool: 10.99.0.1/16}\n", "defaults.pool"},
		{"prefix", "version: 1\ndefaults: {prefix: 12}\n", "defaults.prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("x", []byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v\nwant it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseReportsAllProblems(t *testing.T) {
	_, err := Parse("x", []byte("version: 1\nnodes:\n  a: {}\n  b: {}\n"))
	if err == nil || strings.Count(err.Error(), "set exactly one") != 2 {
		t.Errorf("err = %v, want both nodes reported", err)
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	write := func(d string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(d, FileName), []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WGLINK_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Chdir(dir)

	if _, err := Find(""); err == nil {
		t.Error("Find with no inventory succeeded")
	}
	cfg := filepath.Join(dir, "cfg", "wglink")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	write(cfg)
	if got, _ := Find(""); got != cfg {
		t.Errorf("Find = %q, want the config dir %q", got, cfg)
	}
	write(dir)
	if got, _ := Find(""); got != "." {
		t.Errorf("Find = %q, want the working directory", got)
	}
	other := t.TempDir()
	t.Setenv("WGLINK_DIR", other)
	if _, err := Find(""); err == nil || !strings.Contains(err.Error(), other) {
		t.Errorf("Find with empty WGLINK_DIR err = %v", err)
	}
	write(other)
	if got, _ := Find(""); got != other {
		t.Errorf("Find = %q, want WGLINK_DIR %q", got, other)
	}
	if got, _ := Find(cfg); got != cfg {
		t.Errorf("Find(flag) = %q, want %q", got, cfg)
	}
}

func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"endpont", "endpoint", 1}, {"", "ab", 2}, {"sudo", "sudo", 0}, {"kitten", "sitting", 3}} {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParseIPv6(t *testing.T) {
	for in, want := range map[string]IPv6{
		"auto":                {On: true},
		"true":                {On: true},
		"off":                 {},
		"fd12:3456:789a::/64": {On: true, Subnet: netip.MustParsePrefix("fd12:3456:789a::/64")},
	} {
		yaml := "version: 1\nnetworks:\n  n: {ipv6: '" + in + "', members: []}\n"
		inv, err := Parse("wglink.yaml", []byte(yaml))
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got := inv.Networks["n"].IPv6; got != want {
			t.Errorf("%s: got %+v, want %+v", in, got, want)
		}
	}
	inv, err := Parse("wglink.yaml", []byte("version: 1\nnetworks:\n  n: {ipv6: true, members: []}\n"))
	if err != nil || !inv.Networks["n"].IPv6.On {
		t.Errorf("bare true: %+v %v", inv, err)
	}
}
