package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/config"
)

const base = `version: 1
nodes:
  pve1: {ssh: root@pve1}
  nas: {ssh: admin@nas}
  vps: {ssh: root@vps}
networks:
  lab:
    members:
      pve1: auto
      nas: ".10"
`

func load(t *testing.T, yaml string) (*config.Doc, *config.Inventory) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, doc, inv, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return doc, inv
}

// reload checks the edited document and parses it again.
func reload(t *testing.T, doc *config.Doc) *config.Inventory {
	t.Helper()
	inv, err := doc.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestAddNode(t *testing.T) {
	doc, inv := load(t, base)
	for name, n := range map[string]Node{
		"ct101": {PCT: 101, Via: "pve1", Endpoint: "192.168.1.101"},
		"vm200": {QM: 200, Via: "pve1"},
		"web":   {Exec: []string{"docker", "exec", "-i", "web"}},
		"here":  {Local: true, Sudo: true},
		"phone": {Manual: true, Keepalive: 25},
	} {
		if err := AddNode(doc, inv, name, n); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	got := reload(t, doc)
	if n := got.Nodes["ct101"]; n.PCT == nil || n.PCT.VMID != 101 || n.PCT.Via != "pve1" {
		t.Errorf("ct101 = %+v", n)
	}
	if n := got.Nodes["vm200"]; n.QM == nil || n.QM.VMID != 200 {
		t.Errorf("vm200 = %+v", n)
	}
	if n := got.Nodes["phone"]; n.Reach() != config.ReachManual || n.Endpoint.String() != "none" {
		t.Errorf("phone = %+v, endpoint %s", n, n.Endpoint)
	}
	if n := got.Nodes["here"]; n.Reach() != config.ReachLocal || !n.Sudo {
		t.Errorf("here = %+v", n)
	}

	for want, try := range map[string]struct {
		name string
		n    Node
	}{
		"use lowercase letters":    {"Bad", Node{Local: true}},
		"node nas already exists":  {"nas", Node{Local: true}},
		"say how to reach it":      {"x", Node{}},
		"one way":                  {"x", Node{Local: true, SSH: "x"}},
		"needs the Proxmox host":   {"x", Node{PCT: 5}},
		"isn't a WireGuard public": {"x", Node{Manual: true, PubKey: "nope"}},
		"goes with a manual node":  {"x", Node{SSH: "x", PubKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}},
	} {
		if err := AddNode(doc, inv, try.name, try.n); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", try.name, err, want)
		}
	}
}

func TestConnect(t *testing.T) {
	doc, inv := load(t, base)
	name, err := Connect(doc, inv, Connection{Nodes: []string{"pve1", "vps"}, Fields: []config.Field{{Key: "psk", Value: true}}})
	if err != nil || name != "pve1-vps" {
		t.Fatalf("Connect = %q, %v", name, err)
	}
	if n := reload(t, doc).Networks["pve1-vps"]; n == nil || !n.PSK || len(n.Members) != 2 {
		t.Errorf("pve1-vps = %+v", n)
	}

	// Joining an existing network keeps its members' pinned addresses.
	if _, err := Connect(doc, inv, Connection{Network: "lab", Nodes: []string{"nas", "vps"}}); err != nil {
		t.Fatal(err)
	}
	lab := reload(t, doc).Networks["lab"]
	if m, _ := lab.Member("nas"); m.Addr.String() != ".10" {
		t.Errorf("nas lost its pin: %s", m.Addr)
	}
	if _, ok := lab.Member("vps"); !ok {
		t.Error("vps didn't join")
	}
	// A pin given now applies.
	pin, _ := config.ParseAddrSpec(".20")
	Connect(doc, inv, Connection{Network: "lab", Nodes: []string{"nas"}, Pins: map[string]config.AddrSpec{"nas": pin}})
	if m, _ := reload(t, doc).Networks["lab"].Member("nas"); m.Addr.String() != ".20" {
		t.Errorf("nas = %s, want .20", m.Addr)
	}

	for want, c := range map[string]Connection{
		"no node named ghost":        {Nodes: []string{"pve1", "ghost"}},
		"at least two nodes":         {Network: "solo", Nodes: []string{"pve1"}},
		"up to 12 lowercase letters": {Network: "Way-Too-Long-Name", Nodes: []string{"pve1", "nas"}},
	} {
		if _, err := Connect(doc, inv, c); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: got %v, want %q", c, err, want)
		}
	}
	if n := AutoName(inv, []string{"pve1", "nas", "vps"}); n != "net1" {
		t.Errorf("AutoName = %s", n)
	}
}
