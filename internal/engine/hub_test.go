package engine

import (
	"strings"
	"testing"
)

const hubNet = base + "  lab: {members: [pve1, nas, pi], hub: pve1}\n"

func TestHubNetwork(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(hubNet, Options{})

	hub := l.conf("pve1", "wg-lab")
	for _, want := range []string{
		"PostUp = echo 1 > /proc/sys/net/ipv4/conf/%i/forwarding\n",
		"# nas\nPublicKey = pub0000000000000000000000000000000000000201=\nAllowedIPs = 10.99.0.2/32\n",
		"# pi\nPublicKey = pub0000000000000000000000000000000000000301=\nAllowedIPs = 10.99.0.3/32\n",
	} {
		if !strings.Contains(hub, want) {
			t.Errorf("hub config lacks %q:\n%s", want, hub)
		}
	}
	spoke := l.conf("nas", "wg-lab")
	if strings.Contains(spoke, "PostUp") || strings.Contains(spoke, "# pi") || !strings.Contains(spoke, "# pve1\nPublicKey = pub0000000000000000000000000000000000000101=\nAllowedIPs = 10.99.0.0/24\n") {
		t.Errorf("spoke config:\n%s", spoke)
	}

	// Status only has links to the hub.
	r := l.status(hubNet, StatusOptions{NoPing: true})
	var pairs []string
	for _, k := range r.Networks[0].Links {
		pairs = append(pairs, k.A+"-"+k.B)
	}
	if strings.Join(pairs, " ") != "pve1-nas pve1-pi" {
		t.Errorf("links = %v", pairs)
	}

	// Back to a full mesh: spokes change live, the hub restarts without forwarding.
	mesh := base + "  lab: {members: [pve1, nas, pi]}\n"
	_, p := l.plan(mesh, Options{})
	m := members(p, "lab")
	if m["pve1"].Action != Restart || !strings.Contains(strings.Join(m["pve1"].Reasons, ","), "forwarding off") || m["nas"].Action != Live {
		t.Errorf("plan:\n%s", render(p))
	}
	l.apply(mesh, Options{})
	if c := l.conf("nas", "wg-lab"); !strings.Contains(c, "# pi\n") || !strings.Contains(c, "AllowedIPs = 10.99.0.1/32") {
		t.Errorf("nas after going back to a mesh:\n%s", c)
	}
}

func TestHubMustBeAMember(t *testing.T) {
	_, err := newLab(t, "pve1").sessionErr(base + "  lab: {members: [pve1, nas], hub: pi}\n")
	if err == nil || !strings.Contains(err.Error(), "hub: pi isn't a member") {
		t.Errorf("err = %v", err)
	}
}
