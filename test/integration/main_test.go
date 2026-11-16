//go:build integration

// Package integration runs wglink against real WireGuard interfaces in
// network namespaces. It changes this machine (namespaces, and with
// WGLINK_INTEGRATION_HOST=1 a wg-quick systemd unit), so it runs only with
// WGLINK_INTEGRATION=1. It needs root or passwordless sudo, iproute2,
// wireguard-tools, OpenSSH, and the WireGuard kernel module or wireguard-go.
package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// bin is the wglink binary under test.
var bin string

func TestMain(m *testing.M) {
	if os.Getenv("WGLINK_INTEGRATION") != "1" {
		fmt.Println("skipping: these tests change this machine; set WGLINK_INTEGRATION=1 to run them")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "wglink-it-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin = filepath.Join(dir, "wglink")
	build := exec.Command("go", "build", "-o", bin, "github.com/gureetk/wglink/cmd/wglink")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building wglink:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
