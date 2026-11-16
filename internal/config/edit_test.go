package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustAddr(t *testing.T, s string) AddrSpec {
	t.Helper()
	a, err := ParseAddrSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func render(t *testing.T, d *Doc) string {
	t.Helper()
	b, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditFromTemplate(t *testing.T) {
	d, err := ParseDoc(Template(DefaultPool, 24, 51820))
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(d.SetNode("pve1", Field{"ssh", "root@192.168.1.10"}))
	must(d.SetNode("nas", Field{"ssh", "admin@nas.lan"}, Field{"sudo", true}, Field{"endpoint", "192.168.1.20"}))
	must(d.SetNode("ct101", Field{"pct", Flow{map[string]any{"via": "pve1", "vmid": 101}}}, Field{"endpoint", "192.168.1.101"}))
	must(d.SetNode("web", Field{"exec", Flow{[]string{"docker", "exec", "-i", "web"}}}, Field{"endpoint", "none"}))
	must(d.AddNetwork("lab"))
	for _, n := range []string{"pve1", "nas", "ct101"} {
		must(d.SetMember("lab", n, AddrSpec{}))
	}
	must(d.AddNetwork("backup", Field{"subnet", "172.30.5.0/24"}, Field{"port", 51900}))
	must(d.SetMember("backup", "nas", mustAddr(t, ".10")))
	must(d.SetMember("backup", "pve1", AddrSpec{}))

	want := `# wglink inventory: the machines wglink manages and the networks between them.
# Edit it by hand or with wglink commands, then run "wglink plan" to preview.
version: 1

defaults:
  pool: 10.99.0.0/16 # automatic subnets come from here
  prefix: 24 # size of each automatic subnet
  port: 51820 # first listen port tried

nodes:
  pve1:
    ssh: root@192.168.1.10
  nas:
    ssh: admin@nas.lan
    sudo: true
    endpoint: 192.168.1.20
  ct101:
    pct: {via: pve1, vmid: 101}
    endpoint: 192.168.1.101
  web:
    exec: [docker, exec, -i, web]
    endpoint: none

networks:
  lab:
    members: [pve1, nas, ct101]
  backup:
    subnet: 172.30.5.0/24
    port: 51900
    members: [nas, pve1]
`
	// nas was pinned, so backup's members must be a map.
	want = strings.Replace(want, "    members: [nas, pve1]\n", "    members:\n      nas: \".10\"\n      pve1: auto\n", 1)
	if got := render(t, d); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	inv, err := d.Inventory()
	if err != nil {
		t.Fatalf("edited inventory is invalid: %v", err)
	}
	if m, _ := inv.Networks["backup"].Member("nas"); m.Addr.String() != ".10" {
		t.Errorf("nas in backup = %s", m.Addr)
	}
}

const commented = `version: 1
# Machines.
nodes:
  pve1: # the big one
    ssh: root@pve1 # over the LAN
  pi:
    ssh: pi-garage

networks:
  # Home lab mesh.
  lab:
    members:
      pve1: .10 # pinned
      pi: auto
`

func TestEditKeepsComments(t *testing.T) {
	d, err := ParseDoc([]byte(commented))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetNetworkFields("lab", Field{"mtu", 1380}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMember("lab", "pve1", mustAddr(t, ".20")); err != nil {
		t.Fatal(err)
	}
	got := render(t, d)
	for _, c := range []string{"# Machines.", "# the big one", "# over the LAN", "# Home lab mesh.", "pve1: \".20\" # pinned", "mtu: 1380"} {
		if !strings.Contains(got, c) {
			t.Errorf("missing %q in:\n%s", c, got)
		}
	}
}

func TestRemoveMemberCollapsesToList(t *testing.T) {
	d, err := ParseDoc([]byte(commented))
	if err != nil {
		t.Fatal(err)
	}
	if !d.RemoveMember("lab", "pve1") {
		t.Fatal("pve1 not removed")
	}
	if d.RemoveMember("lab", "pve1") {
		t.Error("second removal reported success")
	}
	if got := render(t, d); !strings.Contains(got, "members: [pi]") {
		t.Errorf("all-auto members should collapse to a list:\n%s", got)
	}
	if !d.RemoveMember("lab", "pi") {
		t.Fatal("pi not removed")
	}
	if got := render(t, d); !strings.Contains(got, "members: []") {
		t.Errorf("want an empty list:\n%s", got)
	}
}

func TestSetMemberIsIdempotent(t *testing.T) {
	d, _ := ParseDoc([]byte("version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n:\n    members: [a]\n"))
	if err := d.SetMember("n", "a", AddrSpec{}); err != nil {
		t.Fatal(err)
	}
	if got := render(t, d); strings.Count(got, "a") != strings.Count("version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n:\n    members: [a]\n", "a") {
		t.Errorf("member added twice:\n%s", got)
	}
}

func TestSetMemberKeepsMappingSpec(t *testing.T) {
	d, _ := ParseDoc([]byte("version: 1\nnodes:\n  a: {ssh: a}\nnetworks:\n  n:\n    members:\n      a: {ip: .5}\n"))
	if err := d.SetMember("n", "a", mustAddr(t, ".6")); err != nil {
		t.Fatal(err)
	}
	if got := render(t, d); !strings.Contains(got, `a: {ip: ".6"}`) {
		t.Errorf("got:\n%s", got)
	}
}

func TestRemoveNodeAndNetwork(t *testing.T) {
	d, _ := ParseDoc([]byte(commented))
	if !d.RemoveNetwork("lab") || d.RemoveNetwork("lab") {
		t.Error("RemoveNetwork should succeed once")
	}
	if !d.RemoveNode("pi") || d.RemoveNode("pi") {
		t.Error("RemoveNode should succeed once")
	}
	got := render(t, d)
	if strings.Contains(got, "pi-garage") || strings.Contains(got, "lab:") {
		t.Errorf("leftovers:\n%s", got)
	}
}

func TestEditErrors(t *testing.T) {
	d, _ := ParseDoc([]byte(commented))
	if err := d.AddNetwork("lab"); err == nil {
		t.Error("AddNetwork on an existing name succeeded")
	}
	if err := d.SetMember("ghost", "pi", AddrSpec{}); err == nil {
		t.Error("SetMember on a missing network succeeded")
	}
	if _, err := ParseDoc([]byte("- a\n- b\n")); err == nil {
		t.Error("ParseDoc accepted a top-level list")
	}
}

func TestEmptyDoc(t *testing.T) {
	d, err := ParseDoc(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetNode("a", Field{"ssh", "a"}); err != nil {
		t.Fatal(err)
	}
	if got := render(t, d); got != "nodes:\n  a:\n    ssh: a\n" {
		t.Errorf("got %q", got)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	d, _ := ParseDoc(Template(DefaultPool, 24, 51820))
	if err := d.SetNode("a"); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(path); err == nil {
		t.Fatal("Save wrote a node with no reach method")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("invalid inventory reached disk")
	}
	d.RemoveNode("a")
	if err := d.SetNode("a", Field{"ssh", "a"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("saved inventory does not load: %v", err)
	}
}

func TestSetNodeFieldsKeepsTheRest(t *testing.T) {
	d, err := ParseDoc([]byte("version: 1\nnodes:\n  phone:\n    manual: {}\n    endpoint: none # behind NAT\n    keepalive: 25\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetNodeFields("phone", Field{"manual", Flow{map[string]any{"pubkey": "K"}}}); err != nil {
		t.Fatal(err)
	}
	want := "version: 1\n\nnodes:\n  phone:\n    manual: {pubkey: K}\n    endpoint: none # behind NAT\n    keepalive: 25\n"
	if got := render(t, d); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if err := d.SetNodeFields("ghost", Field{"sudo", true}); err == nil {
		t.Error("changed a node that doesn't exist")
	}
}

func TestSetMemberRoutes(t *testing.T) {
	d, err := ParseDoc([]byte("version: 1\nnodes:\n  a: {ssh: a}\n  b: {ssh: b}\nnetworks:\n  lab:\n    members: [a, b]\n"))
	if err != nil {
		t.Fatal(err)
	}
	lan := []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24")}
	if err := d.SetMemberRoutes("lab", "b", lan); err != nil {
		t.Fatal(err)
	}
	if got := render(t, d); !strings.Contains(got, "      a: auto\n      b: {routes: [192.168.50.0/24]}\n") {
		t.Errorf("with routes:\n%s", got)
	}
	inv, err := d.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := inv.Networks["lab"].Member("b"); len(m.Routes) != 1 || m.Routes[0] != lan[0] {
		t.Errorf("b = %+v", m)
	}
	// A pinned address survives, and clearing collapses back to a list.
	d.SetMember("lab", "b", mustAddr(t, ".10"))
	if got := render(t, d); !strings.Contains(got, `b: {routes: [192.168.50.0/24], ip: ".10"}`) {
		t.Errorf("with an address:\n%s", got)
	}
	d.SetMemberRoutes("lab", "b", nil)
	if got := render(t, d); !strings.Contains(got, "      b: \".10\"\n") {
		t.Errorf("routes cleared:\n%s", got)
	}
	d.SetMember("lab", "b", AddrSpec{})
	d.SetMemberRoutes("lab", "b", nil)
	if got := render(t, d); !strings.Contains(got, "members: [a, b]") {
		t.Errorf("back to a list:\n%s", got)
	}
	if err := d.SetMemberRoutes("lab", "ghost", lan); err == nil {
		t.Error("routes for a non-member")
	}
}
