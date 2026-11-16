//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestHub checks that spokes reach each other through the hub, which
// forwards between them.
func TestHub(t *testing.T) {
	l := newLab(t)
	l.addExecNode("alpha", 1)
	l.addExecNode("bravo", 2)
	l.addExecNode("charlie", 3)
	l.wglink("connect", "alpha", "bravo", "charlie", "--name", "hub", "--hub", "alpha")
	l.wglink("apply", "--yes")
	addrs := l.addrs("hub")

	if peers := strings.Fields(l.mustOn("bravo", "wg", "show", "wg-hub", "peers")); len(peers) != 1 {
		t.Errorf("bravo should only have the hub as a peer: %v", peers)
	}
	if got := strings.TrimSpace(l.mustOn("alpha", "cat", "/proc/sys/net/ipv4/conf/wg-hub/forwarding")); got != "1" {
		t.Errorf("forwarding on the hub's interface = %q", got)
	}
	l.ping("bravo", addrs["charlie"])
	l.ping("charlie", addrs["bravo"])
	l.ping("bravo", addrs["alpha"])

	status, err := l.tryWglink("status", "--exit-code")
	if err != nil || !strings.Contains(status, "All 2 links are ok.") {
		t.Errorf("status: %v\n%s", err, status)
	}
}
