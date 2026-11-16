package engine

import (
	"os"
	"strings"
	"testing"
)

const routedMesh = base + "  lab:\n    members:\n      pve1: auto\n      nas: {routes: [192.168.50.0/24]}\n"

func TestRoutes(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(routedMesh, Options{})
	if c := l.conf("pve1", "wg-lab"); !strings.Contains(c, "# nas\nPublicKey = pub0000000000000000000000000000000000000201=\nAllowedIPs = 10.99.0.2/32, 192.168.50.0/24\n") {
		t.Errorf("pve1:\n%s", c)
	}
	if c := l.conf("nas", "wg-lab"); strings.Contains(c, "192.168.50.0/24") {
		t.Errorf("nas routes its own subnet to itself:\n%s", c)
	}

	// A new route needs a restart, since wg-quick adds routes only at start.
	more := base + "  lab:\n    members:\n      pve1: {routes: [192.168.60.0/24]}\n      nas: {routes: [192.168.50.0/24]}\n"
	_, p := l.plan(more, Options{})
	if m := members(p, "lab"); m["nas"].Action != Restart || !strings.Contains(strings.Join(m["nas"].Reasons, ","), "routes changed") {
		t.Errorf("plan:\n%s", render(p))
	}

	// The routing member is warned when it won't forward.
	b, _ := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	warned := strings.Contains(strings.Join(p.Warnings, "\n"), "IP forwarding is off there")
	if off := strings.TrimSpace(string(b)) != "1"; warned != off {
		t.Errorf("forwarding warning %v, but ip_forward is %q", warned, b)
	}
}

func TestRoutesThroughAHub(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	yaml := base + "  lab:\n    hub: pve1\n    members:\n      pve1: {routes: [192.168.10.0/24]}\n      nas: {routes: [192.168.50.0/24]}\n      pi: auto\n"
	l.apply(yaml, Options{})
	if c := l.conf("pi", "wg-lab"); !strings.Contains(c, "# pve1\nPublicKey = pub0000000000000000000000000000000000000101=\nAllowedIPs = 10.99.0.0/24, 192.168.10.0/24, 192.168.50.0/24\n") {
		t.Errorf("pi:\n%s", c)
	}
	if c := l.conf("pve1", "wg-lab"); !strings.Contains(c, "AllowedIPs = 10.99.0.2/32, 192.168.50.0/24\n") {
		t.Errorf("pve1:\n%s", c)
	}
	if c := l.conf("nas", "wg-lab"); !strings.Contains(c, "AllowedIPs = 10.99.0.0/24, 192.168.10.0/24\n") {
		t.Errorf("nas should get everything but its own subnet through the hub:\n%s", c)
	}
}

func TestRoutesClashWithAMembersOwnSubnet(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.fakes["pve1"].WriteStub("addr", "3: eth1    inet 192.168.50.10/24 brd 192.168.50.255 scope global eth1\n")
	_, p := l.plan(routedMesh, Options{})
	if !strings.Contains(strings.Join(p.Problems, "\n"), "pve1 is on 192.168.50.0/24 itself (address on eth1), so it can't also reach it through nas") {
		t.Errorf("problems: %v", p.Problems)
	}
	// Auto subnets keep clear of routed ones.
	clash := base + "  lab:\n    members:\n      pve1: auto\n      nas: {routes: [10.99.0.0/24]}\n"
	if _, p := l.plan(clash, Options{}); p.Networks[0].Subnet.String() != "10.99.1.0/24" {
		t.Errorf("subnet = %s", p.Networks[0].Subnet)
	}
}
