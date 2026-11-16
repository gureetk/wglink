package node

import (
	"strconv"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/nodetest"
)

// fakePVE makes a fake Proxmox host pve1 in a two-node cluster.
func fakePVE(e *env) {
	e.WriteStub("pvesh_cluster_status", `[{"type":"cluster","name":"lab","nodes":2},
		{"type":"node","name":"pve2","local":0,"online":1},
		{"type":"node","name":"pve1","local":1,"online":1}]`)
	e.WriteStub("pvesh_cluster_resources", `[
		{"id":"qemu/200","type":"qemu","vmid":200,"name":"web","node":"pve1","status":"running","template":0},
		{"id":"lxc/101","type":"lxc","vmid":101,"name":"nas","node":"pve1","status":"running"},
		{"id":"qemu/201","type":"qemu","vmid":201,"name":"win","node":"pve1","status":"running"},
		{"id":"lxc/102","type":"lxc","vmid":102,"name":"old","node":"pve1","status":"stopped"},
		{"id":"lxc/9000","type":"lxc","vmid":9000,"name":"tmpl","node":"pve1","status":"stopped","template":1},
		{"id":"qemu/300","type":"qemu","vmid":300,"name":"elsewhere","node":"pve2","status":"running"}]`)
	e.WriteStub("pvesh_nodes_pve1_lxc_101_interfaces", `[{"name":"lo","inet":"127.0.0.1/8"},
		{"name":"eth0","inet":"192.168.1.101/24","hwaddr":"bc:24:11:00:00:01"},
		{"name":"docker0","inet":"172.17.0.1/16"}]`)
	e.WriteStub("pvesh_nodes_pve1_qemu_200_agent_network-get-interfaces", `{"result":[
		{"name":"lo","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4","prefix":8}]},
		{"name":"ens18","ip-addresses":[{"ip-address":"192.168.1.200","ip-address-type":"ipv4","prefix":24},
			{"ip-address":"fe80::be24:11ff:fe00:2","ip-address-type":"ipv6","prefix":64}]}]}`)
	e.WriteStub("pvesh_nodes_pve1_qemu_201_agent_network-get-interfaces.err", "QEMU guest agent is not running\n")
}

func TestDiscover(t *testing.T) {
	e := setup(t, nodetest.Shell(t), "none")
	fakePVE(e)
	host, guests, err := e.n.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if host != "pve1" {
		t.Errorf("host = %q", host)
	}
	var got []string
	for _, g := range guests {
		got = append(got, strings.TrimSpace(strings.Join([]string{g.Type, strconv.Itoa(g.VMID), g.Name, g.Status, prefixes(g), g.Err}, " ")))
	}
	want := []string{
		"lxc 101 nas running 192.168.1.101/24",
		"lxc 102 old stopped",
		"qemu 200 web running 192.168.1.200/24",
		"qemu 201 win running  QEMU guest agent is not running",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("guests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if log := e.Log(); strings.Contains(log, "/102/") || strings.Contains(log, "/300/") {
		t.Errorf("asked about a stopped or remote guest:\n%s", log)
	}
}

func TestDiscoverNeedsProxmox(t *testing.T) {
	e := setup(t, nodetest.Shell(t), "none")
	if _, _, err := e.n.Discover(ctx); err == nil || !strings.Contains(err.Error(), "no such path") {
		t.Errorf("err = %v", err)
	}
	e.WriteStub("pvesh_cluster_status", `[{"type":"node","name":"pve1"}]`)
	e.WriteStub("pvesh_cluster_resources", `[]`)
	if _, _, err := e.n.Discover(ctx); err == nil || !strings.Contains(err.Error(), "doesn't name this host") {
		t.Errorf("err = %v", err)
	}
}

func prefixes(g Guest) string {
	var s []string
	for _, p := range g.Addrs {
		s = append(s, p.String())
	}
	return strings.Join(s, ",")
}
