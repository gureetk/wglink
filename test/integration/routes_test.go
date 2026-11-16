//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestRoutedSubnet puts a LAN host behind charlie and reaches it from the
// other members through charlie.
func TestRoutedSubnet(t *testing.T) {
	l := newLab(t)
	l.addExecNode("alpha", 1)
	l.addExecNode("bravo", 2)
	l.addExecNode("charlie", 3)

	// lanhost sits on charlie's LAN, 10.250.0.0/24, with charlie as its gateway.
	const lanNS = "wgl-lanhost"
	l.removeNetns(lanNS)
	l.root("ip", "netns", "add", lanNS)
	t.Cleanup(func() { l.removeNetns(lanNS) })
	l.root("ip", "link", "add", "lan0", "netns", "wgl-charlie", "type", "veth", "peer", "name", "eth0", "netns", lanNS)
	l.root("ip", "-n", "wgl-charlie", "addr", "add", "10.250.0.1/24", "dev", "lan0")
	l.root("ip", "-n", "wgl-charlie", "link", "set", "dev", "lan0", "up")
	l.root("ip", "-n", lanNS, "addr", "add", "10.250.0.2/24", "dev", "eth0")
	l.root("ip", "-n", lanNS, "link", "set", "dev", "eth0", "up")
	l.root("ip", "-n", lanNS, "route", "add", "default", "via", "10.250.0.1")
	l.mustOn("charlie", "sh", "-c", "echo 1 > /proc/sys/net/ipv4/ip_forward")

	l.wglink("connect", "alpha", "bravo", "charlie", "--name", "lan")
	l.wglink("routes", "lan", "charlie", "10.250.0.0/24")
	if plan := l.wglink("plan"); strings.Contains(plan, "forwarding is off") {
		t.Errorf("plan warns about forwarding:\n%s", plan)
	}
	l.wglink("apply", "--yes")
	l.ping("alpha", "10.250.0.2")
	l.ping("bravo", "10.250.0.2")

	// Dropping the route takes it off the peers again.
	l.wglink("routes", "lan", "charlie")
	l.wglink("apply", "--yes")
	if out, err := l.on("alpha", "ping", "-c", "1", "-W", "1", "10.250.0.2"); err == nil {
		t.Errorf("alpha still reaches the LAN after the route went:\n%s", out)
	}
}
