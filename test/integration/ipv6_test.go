//go:build integration

package integration

import (
	"os"
	"strings"
	"testing"
)

// TestIPv6 checks that members reach each other over IPv6 in the tunnel, and
// through the hub in a hub network.
func TestIPv6(t *testing.T) {
	if _, err := os.Stat("/proc/net/if_inet6"); err != nil {
		t.Skip("this kernel has no IPv6")
	}
	l := newLab(t)
	l.addExecNode("alpha", 1)
	l.addExecNode("bravo", 2)
	l.addExecNode("charlie", 3)
	l.wglink("connect", "alpha", "bravo", "charlie", "--name", "six", "--ipv6=fd77:1:2::/64", "--hub", "alpha")
	l.wglink("apply", "--yes")

	if out := l.mustOn("bravo", "ip", "-6", "-o", "addr", "show", "dev", "wg-six"); !strings.Contains(out, "fd77:1:2::2/64") {
		t.Errorf("bravo's IPv6 address:\n%s", out)
	}
	l.ping("bravo", "fd77:1:2::1")
	// Older kernels forward IPv6 only when it's on for the whole machine.
	if _, err := l.on("alpha", "test", "-e", "/proc/sys/net/ipv6/conf/wg-six/force_forwarding"); err != nil {
		l.mustOn("alpha", "sysctl", "-qw", "net.ipv6.conf.all.forwarding=1")
	}
	l.ping("bravo", "fd77:1:2::3") // through the hub
	l.ping("charlie", "fd77:1:2::2")
	l.pingAll([]string{"alpha", "bravo", "charlie"}, l.addrs("six"))
}
