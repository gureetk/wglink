//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMesh builds a mesh over real WireGuard, changes it the ways people do,
// and checks traffic flows after each step.
func TestMesh(t *testing.T) {
	l := newLab(t)
	l.addExecNode("alpha", 1)
	l.addExecNode("bravo", 2)
	l.addSSHNode("charlie", 3)
	members := []string{"alpha", "bravo", "charlie"}
	if l.addHostNode("delta", "wg-lab") {
		members = append(members, "delta")
	}
	links := len(members) * (len(members) - 1) / 2

	l.wglink(append(append([]string{"connect"}, members...), "--name", "lab")...)
	plan := l.wglink("plan")
	for _, want := range []string{"network lab   wg-lab   10.99.0.0/24   udp/51820", fmt.Sprintf("%d to create", len(members))} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	l.wglink("apply", "--yes")
	addrs := l.addrs("lab")
	t.Logf("addresses: %v", addrs)
	l.pingAll(members, addrs)

	status := l.wglink("status", "--exit-code")
	if !strings.Contains(status, fmt.Sprintf("All %d links are ok.", links)) {
		t.Errorf("status:\n%s", status)
	}
	if plan := l.wglink("plan"); !strings.Contains(plan, "Everything is up to date.") {
		t.Errorf("plan after apply:\n%s", plan)
	}

	t.Run("keys stay on their nodes", func(t *testing.T) {
		l := l.in(t)
		state, _ := os.ReadFile(filepath.Join(l.dir, "wglink.state.json"))
		inv, _ := os.ReadFile(filepath.Join(l.dir, "wglink.yaml"))
		for _, name := range members {
			key := strings.TrimSpace(l.nodeFile(name, "wg-lab.key"))
			if len(key) != 44 {
				t.Fatalf("%s: odd key %q", name, key)
			}
			for what, text := range map[string]string{"state": string(state), "inventory": string(inv), "output": l.seen.String()} {
				if strings.Contains(text, key) {
					t.Errorf("%s's private key is in the %s", name, what)
				}
			}
			if conf := l.nodeFile(name, "wg-lab.conf"); !strings.Contains(conf, key) {
				t.Errorf("%s's config lacks its private key", name)
			}
		}
	})

	t.Run("peer changes load live", func(t *testing.T) {
		l := l.in(t)
		before := l.ifindex("alpha", "wg-lab")
		l.wglink("net", "set", "lab", "--keepalive", "25")
		plan := l.wglink("plan")
		if !strings.Contains(plan, "(live)") || strings.Contains(plan, "(restart)") {
			t.Errorf("plan:\n%s", plan)
		}
		l.wglink("apply", "--yes")
		if got := l.mustOn("alpha", "wg", "show", "wg-lab", "persistent-keepalive"); strings.Count(got, "\t25") != len(members)-1 {
			t.Errorf("keepalives on alpha:\n%s", got)
		}
		if after := l.ifindex("alpha", "wg-lab"); after != before {
			t.Errorf("alpha's interface was recreated (%s, then %s)", before, after)
		}
		l.pingAll(members, addrs)
	})

	t.Run("a new address restarts only its owner", func(t *testing.T) {
		l := l.in(t)
		before := l.ifindex("alpha", "wg-lab")
		l.wglink("ip", "lab", "charlie", ".50")
		plan := l.wglink("plan")
		if !strings.Contains(plan, "charlie  10.99.0.50") || !strings.Contains(plan, "(restart)") || !strings.Contains(plan, "(live)") {
			t.Errorf("plan:\n%s", plan)
		}
		l.wglink("apply", "--yes")
		addrs = l.addrs("lab")
		if addrs["charlie"] != "10.99.0.50" {
			t.Fatalf("charlie = %s", addrs["charlie"])
		}
		l.pingAll(members, addrs)
		if after := l.ifindex("alpha", "wg-lab"); after != before {
			t.Errorf("alpha's interface was recreated (%s, then %s)", before, after)
		}
	})

	t.Run("hand edits show as drift until apply", func(t *testing.T) {
		l := l.in(t)
		conf := filepath.Join(l.nodes["bravo"].wgDir, "wg-lab.conf")
		l.root("sh", "-c", "echo '# edited by hand' >> "+conf)
		status := l.wglink("status")
		if !strings.Contains(status, "edited on the node since the last apply") {
			t.Errorf("status:\n%s", status)
		}
		if plan := l.wglink("plan"); !strings.Contains(plan, "edited on the node since the last apply") {
			t.Errorf("plan:\n%s", plan)
		}
		l.wglink("apply", "--yes")
		if strings.Contains(l.nodeFile("bravo", "wg-lab.conf"), "edited by hand") {
			t.Error("apply left the hand edit")
		}
		if plan := l.wglink("plan"); !strings.Contains(plan, "Everything is up to date.") {
			t.Errorf("plan:\n%s", plan)
		}
	})

	t.Run("status on a node needs no inventory", func(t *testing.T) {
		l := l.in(t)
		out := l.mustOn("alpha", bin, "status", "--local", "--exit-code")
		if !strings.Contains(out, "interface wg-lab   network lab   "+addrs["alpha"]+"/24") || !strings.Contains(out, "  bravo ") || !strings.Contains(out, "  charlie ") {
			t.Errorf("status --local on alpha:\n%s", out)
		}
	})

	t.Run("a manual peer runs its exported config", func(t *testing.T) {
		l := l.in(t)
		// echo stands in for a phone: wglink never runs anything there.
		l.netnsNode("echo", 9)
		l.wglink("node", "add", "echo", "--manual", "--keepalive", "25")
		l.wglink("net", "join", "lab", "echo")
		conf := filepath.Join(l.nodes["echo"].wgDir, "wg-lab.conf")
		l.wglink("export", "lab", "echo", "-o", conf)
		l.wglink("apply", "--yes")
		l.mustOn("echo", "wg-quick", "up", "wg-lab")
		addrs := l.addrs("lab")
		for _, name := range members {
			l.ping("echo", addrs[name])
			l.ping(name, addrs["echo"])
		}
		status, err := l.tryWglink("status", "--exit-code")
		if err != nil || !strings.Contains(status, "echo     "+addrs["echo"]) || !strings.Contains(status, "manual") {
			t.Errorf("status: %v\n%s", err, status)
		}
		if plan := l.wglink("plan"); !strings.Contains(plan, "Everything is up to date.") {
			t.Errorf("plan:\n%s", plan)
		}
		l.wglink("net", "leave", "lab", "echo")
		l.wglink("apply", "--yes")
		l.wglink("node", "rm", "echo")
		if peers := strings.Fields(l.mustOn("alpha", "wg", "show", "wg-lab", "peers")); len(peers) != len(members)-1 {
			t.Errorf("alpha's peers after echo left: %v", peers)
		}
	})

	t.Run("a lost state file is rebuilt from the nodes", func(t *testing.T) {
		l := l.in(t)
		path := filepath.Join(l.dir, "wglink.state.json")
		want, _ := os.ReadFile(path)
		os.Remove(path)
		out := l.wglink("state", "rebuild")
		got, _ := os.ReadFile(path)
		if !sameJSON(got, want) {
			t.Errorf("rebuilt state:\n%s\nwant:\n%s\noutput:\n%s", got, want, out)
		}
		if plan := l.wglink("plan"); !strings.Contains(plan, "Everything is up to date.") {
			t.Errorf("plan after rebuild:\n%s", plan)
		}
	})

	t.Run("leaving takes the tunnel down", func(t *testing.T) {
		l := l.in(t)
		l.wglink("net", "leave", "lab", "charlie")
		l.wglink("apply", "--yes")
		if out, err := l.on("charlie", "ip", "link", "show", "wg-lab"); err == nil {
			t.Errorf("charlie still has wg-lab:\n%s", out)
		}
		if peers := strings.Fields(l.mustOn("alpha", "wg", "show", "wg-lab", "peers")); len(peers) != len(members)-2 {
			t.Errorf("alpha's peers: %v", peers)
		}
		rest := slices.DeleteFunc(slices.Clone(members), func(n string) bool { return n == "charlie" })
		l.pingAll(rest, addrs)
		if _, err := l.tryWglink("status", "--exit-code"); err != nil {
			t.Errorf("status after leave: %v", err)
		}
	})

	t.Run("removing the network removes every tunnel", func(t *testing.T) {
		l := l.in(t)
		l.wglink("net", "rm", "lab")
		l.wglink("apply", "--yes")
		for _, name := range members {
			if out, err := l.on(name, "ip", "link", "show", "wg-lab"); err == nil {
				t.Errorf("%s still has wg-lab:\n%s", name, out)
			}
		}
	})
}

// pingAll checks every member reaches every other through the tunnel.
func (l *lab) pingAll(members []string, addrs map[string]string) {
	l.t.Helper()
	for _, a := range members {
		for _, b := range members {
			if a != b {
				l.ping(a, addrs[b])
			}
		}
	}
}

// sameJSON reports whether two JSON documents hold the same data.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}
