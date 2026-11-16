//go:build integration

package integration

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/runner"
	"github.com/gureetk/wglink/internal/wgconf"
)

// TestRevertTimer runs the undo timer under this machine's real systemd.
func TestRevertTimer(t *testing.T) {
	if os.Getenv("WGLINK_INTEGRATION_HOST") != "1" {
		t.Skip("set WGLINK_INTEGRATION_HOST=1 to run: it adds wg-quick and timer units to this machine")
	}
	ctx := context.Background()
	host := node.Node{Runner: runner.Local{Sudo: true}, Service: "systemd"}
	root := func(argv ...string) (string, error) {
		argv = append(sudo(), argv...)
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	for _, iface := range []string{"wgl-revert", "wgl-revert2"} {
		cleanup := func() {
			root("systemctl", "stop", "wglink-revert-"+iface+".timer")
			root("systemctl", "disable", "--now", "wg-quick@"+iface)
			root("sh", "-c", "rm -f /etc/wireguard/"+iface+".* /etc/wireguard/."+iface+".revert")
		}
		cleanup()
		t.Cleanup(cleanup)
		if _, _, err := host.EnsureKey(ctx, iface); err != nil {
			t.Fatal(err)
		}
	}
	config := func(addr string, port uint16) []byte {
		return wgconf.Render(wgconf.Interface{Network: "revert", Addresses: []netip.Prefix{netip.MustParsePrefix(addr)}, ListenPort: port})
	}
	port := func(iface string) string {
		out, _ := root("wg", "show", iface, "listen-port")
		return out
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for start := time.Now(); !ok(); time.Sleep(250 * time.Millisecond) {
			if time.Since(start) > 30*time.Second {
				t.Fatalf("gave up waiting for %s", what)
			}
		}
	}

	if _, err := host.Write(ctx, node.Write{Iface: "wgl-revert", Config: config("198.18.5.1/24", 51990)}); err != nil {
		t.Fatal(err)
	}
	if got := port("wgl-revert"); got != "51990" {
		t.Fatalf("listen port %q", got)
	}

	// Not confirmed: systemd puts the old config back.
	if _, err := host.Write(ctx, node.Write{Iface: "wgl-revert", Config: config("198.18.5.1/24", 51991), Revert: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if got := port("wgl-revert"); got != "51991" {
		t.Fatalf("listen port after the change %q", got)
	}
	waitFor("the timer to undo the change", func() bool { return port("wgl-revert") == "51990" })
	if err := host.Confirm(ctx, "wgl-revert"); err == nil || !strings.Contains(err.Error(), "already undid") {
		t.Errorf("confirm after the timer: %v", err)
	}

	// Confirmed: the timer is gone and the change stays.
	if _, err := host.Write(ctx, node.Write{Iface: "wgl-revert", Config: config("198.18.5.1/24", 51992), Revert: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := host.Confirm(ctx, "wgl-revert"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	if got := port("wgl-revert"); got != "51992" {
		t.Errorf("listen port after confirming %q", got)
	}
	if out, _ := root("systemctl", "is-active", "wglink-revert-wgl-revert.timer"); out == "active" {
		t.Error("the timer is still active")
	}

	// A new interface that isn't confirmed is taken down again.
	if _, err := host.Write(ctx, node.Write{Iface: "wgl-revert2", Config: config("198.18.6.1/24", 51993), Revert: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	// The script removes the config just after the interface goes.
	waitFor("the new interface and its config to go", func() bool {
		_, link := root("ip", "link", "show", "wgl-revert2")
		_, conf := root("test", "-e", "/etc/wireguard/wgl-revert2.conf")
		return link != nil && conf != nil
	})
}
