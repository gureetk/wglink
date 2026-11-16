package cli

import (
	"regexp"
	"strings"
	"testing"
)

func TestDiscover(t *testing.T) {
	dir, fakes := fakeLab(t)
	pve := fakes["pve1"]
	pve.WriteStub("pvesh_cluster_status", `[{"type":"node","name":"pve1","local":1}]`)
	pve.WriteStub("pvesh_cluster_resources", `[
		{"type":"lxc","vmid":101,"name":"nas","node":"pve1","status":"running"},
		{"type":"lxc","vmid":102,"name":"old","node":"pve1","status":"stopped"},
		{"type":"qemu","vmid":200,"name":"Web_Server","node":"pve1","status":"running"},
		{"type":"qemu","vmid":201,"name":"win","node":"pve1","status":"running"}]`)
	pve.WriteStub("pvesh_nodes_pve1_lxc_101_interfaces", `[{"name":"eth0","inet":"192.168.1.101/24"}]`)
	pve.WriteStub("pvesh_nodes_pve1_qemu_200_agent_network-get-interfaces", `{"result":[{"name":"ens18","ip-addresses":[{"ip-address":"192.168.1.200","prefix":24}]}]}`)
	pve.WriteStub("pvesh_nodes_pve1_qemu_201_agent_network-get-interfaces.err", "QEMU guest agent is not running\n")

	out := wg(t, dir, "discover", "pve1")
	for _, want := range []string{
		`101 +lxc +nas +running +192\.168\.1\.101 +nas-101 \(new\)`, // nas is taken
		`102 +lxc +old +stopped +- +-`,
		`200 +qemu +Web_Server +running +192\.168\.1\.200 +web-server \(new\)`,
		`201 +qemu +win +running +- +QEMU guest agent is not running`,
		`Run "wglink discover pve1 --add" to add the new ones as nodes\.`,
	} {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("discover lacks %s:\n%s", want, out)
		}
	}
	if _, ok := load(t, dir).Nodes["web-server"]; ok {
		t.Error("discover without --add changed wglink.yaml")
	}

	out = wg(t, dir, "discover", "pve1", "--add")
	if !strings.Contains(out, "Added nas-101, web-server.") || !strings.Contains(out, "Left out win 201 (QEMU guest agent is not running)") {
		t.Errorf("discover --add:\n%s", out)
	}
	inv := load(t, dir)
	if n := inv.Nodes["nas-101"]; n == nil || n.PCT == nil || n.PCT.Via != "pve1" || n.PCT.VMID != 101 || n.Endpoint.Host != "192.168.1.101" {
		t.Errorf("nas-101 = %+v", n)
	}
	if n := inv.Nodes["web-server"]; n == nil || n.QM == nil || n.QM.VMID != 200 || n.Endpoint.Host != "192.168.1.200" {
		t.Errorf("web-server = %+v", n)
	}

	out = wg(t, dir, "discover", "pve1")
	if !regexp.MustCompile(`101 +lxc +nas +running +192\.168\.1\.101 +nas-101\n`).MatchString(out) || !strings.Contains(out, "Nothing new to add.") {
		t.Errorf("discover after --add:\n%s", out)
	}
	wgErr(t, dir, "pvesh get /cluster/status failed", "discover", "nas")
	wgErr(t, dir, "no node named ghost", "discover", "ghost")
}
