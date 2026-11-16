//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestPresharedKeys checks that each pair shares a preshared key, that the
// tunnels come up with them, that rotating a member replaces its keys, and
// that turning them off takes effect live.
func TestPresharedKeys(t *testing.T) {
	l := newLab(t)
	l.addExecNode("alpha", 1)
	l.addExecNode("bravo", 2)
	l.addExecNode("charlie", 3)
	members := []string{"alpha", "bravo", "charlie"}
	l.wglink("connect", "alpha", "bravo", "charlie", "--name", "psk", "--psk", "--keepalive", "25")
	l.wglink("apply", "--yes")
	addrs := l.addrs("psk")

	// key is the preshared key a node stored for peer; live is the set its
	// interface is using.
	key := func(name, peer string) string { return strings.TrimSpace(l.nodeFile(name, "wg-psk.psk."+peer)) }
	live := func(name string) string { return l.mustOn(name, "wg", "show", "wg-psk", "preshared-keys") }
	check := func() {
		t.Helper()
		for _, a := range members {
			for _, b := range members {
				if a != b && (key(a, b) != key(b, a) || !strings.Contains(live(a), "\t"+key(a, b))) {
					t.Errorf("%s and %s don't share a loaded key:\n%s\n%s", a, b, live(a), live(b))
				}
			}
		}
		l.pingAll(members, addrs)
	}
	check()
	if strings.Contains(live("alpha"), "(none)") {
		t.Errorf("alpha has a peer without a preshared key:\n%s", live("alpha"))
	}

	old, kept := key("alpha", "bravo"), key("bravo", "charlie")
	l.wglink("rotate", "psk", "alpha", "--yes")
	if key("alpha", "bravo") == old || key("bravo", "charlie") != kept {
		t.Error("rotating alpha didn't replace just its preshared keys")
	}
	check()

	// wg syncconf keeps settings a config leaves out, unless wglink zeroes them.
	index := l.ifindex("alpha", "wg-psk")
	l.wglink("net", "set", "psk", "--psk=false", "--keepalive", "0")
	l.wglink("apply", "--yes")
	if l.ifindex("alpha", "wg-psk") != index {
		t.Error("turning preshared keys off restarted the interface")
	}
	for _, n := range members {
		if ka := l.mustOn(n, "wg", "show", "wg-psk", "persistent-keepalive"); strings.Count(ka, "off") != 2 {
			t.Errorf("%s still sends keepalives:\n%s", n, ka)
		}
		if out := l.mustOn(n, "sh", "-c", "ls /etc/wireguard"); strings.Contains(out, ".psk.") {
			t.Errorf("%s kept key files: %s", n, out)
		}
		if strings.Count(live(n), "(none)") != 2 {
			t.Errorf("%s still loads preshared keys:\n%s", n, live(n))
		}
	}
	l.pingAll(members, addrs)
}
