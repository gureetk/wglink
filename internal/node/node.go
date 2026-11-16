// Package node runs wglink's fixed shell scripts on a node and parses what
// they print. Scripts are POSIX sh, embedded in the binary, and fed to
// "sh -s" on stdin; only the WG_* variables vary, and every value is
// validated and quoted.
package node

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/gureetk/wglink/internal/runner"
	"github.com/gureetk/wglink/internal/wgconf"
)

//go:embed scripts/*.sh
var scripts embed.FS

// DefaultDir is where wg-quick looks for configs.
const DefaultDir = "/etc/wireguard"

// Node runs scripts on one machine.
type Node struct {
	Runner  runner.Runner
	Dir     string // config directory; DefaultDir when empty
	Service string // auto, systemd or none
}

var (
	ifaceRE = regexp.MustCompile(`^[a-zA-Z0-9_=+.-]{1,15}$`)
	nameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// run builds the named script with vars and runs it.
func (n Node) run(ctx context.Context, name string, vars map[string]string) ([]byte, error) {
	dir := n.Dir
	if dir == "" {
		dir = DefaultDir
	}
	service := n.Service
	if service == "" {
		service = "auto"
	}
	all := map[string]string{"WG_DIR": dir, "WG_SERVICE": service}
	for k, v := range vars {
		all[k] = v
	}
	script, err := build(name, all)
	if err != nil {
		return nil, err
	}
	return n.Runner.Run(ctx, []string{"sh", "-s"}, script)
}

// build assembles a complete script: settings, variables, helpers, then main.
func build(name string, vars map[string]string) ([]byte, error) {
	lib, err := scripts.ReadFile("scripts/lib.sh")
	if err != nil {
		return nil, err
	}
	body, err := scripts.ReadFile("scripts/" + name + ".sh")
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("set -eu\numask 077\n")
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, runner.Quote([]string{vars[k]}))
	}
	b.Write(lib)
	b.Write(body)
	b.WriteString("main\n")
	return b.Bytes(), nil
}

func checkIfaces(ifaces []string) error {
	for _, i := range ifaces {
		if !ifaceRE.MatchString(i) {
			return fmt.Errorf("invalid interface name %q", i)
		}
	}
	return nil
}

// EnsureKey creates the interface's private key if missing and returns the public key.
func (n Node) EnsureKey(ctx context.Context, iface string) (pub string, created bool, err error) {
	if err := checkIfaces([]string{iface}); err != nil {
		return "", false, err
	}
	out, err := n.run(ctx, "key", map[string]string{"WG_IFACE": iface})
	if err != nil {
		return "", false, err
	}
	for _, d := range parse(out) {
		switch d.key {
		case "created":
			created = true
		case "pub":
			pub = d.arg
		}
	}
	if !ValidKey(pub) {
		return "", false, fmt.Errorf("%s: got no valid public key for %s", n.Runner, iface)
	}
	return pub, created, nil
}

// Write is a config to install.
type Write struct {
	Iface  string
	Config []byte        // from wgconf.Render
	Mode   wgconf.Change // Live or Restart
	Adopt  bool          // replace a config wglink didn't write
}

// Write installs and loads a config, and returns its public form as now on disk.
func (n Node) Write(ctx context.Context, w Write) ([]byte, error) {
	if err := checkIfaces([]string{w.Iface}); err != nil {
		return nil, err
	}
	if !wgconf.IsManaged(w.Config) {
		return nil, fmt.Errorf("config for %s lacks the wglink marker", w.Iface)
	}
	// Peer names become file names on the node; refuse anything odd.
	for _, l := range strings.Split(string(w.Config), "\n") {
		if peer, ok := strings.CutPrefix(l, "PresharedKey = @"); ok && !nameRE.MatchString(peer) {
			return nil, fmt.Errorf("config for %s: invalid peer name %q", w.Iface, peer)
		}
	}
	mode := "restart"
	if w.Mode == wgconf.Live {
		mode = "live"
	}
	adopt := "0"
	if w.Adopt {
		adopt = "1"
	}
	out, err := n.run(ctx, "write", map[string]string{
		"WG_IFACE": w.Iface, "WG_CONFIG": string(w.Config), "WG_MODE": mode, "WG_ADOPT": adopt,
	})
	if err != nil {
		return nil, err
	}
	for _, d := range parse(out) {
		if d.key == "section" && d.arg == "config" {
			return d.raw, nil
		}
	}
	return nil, fmt.Errorf("%s: write of %s printed no config", n.Runner, w.Iface)
}

// Remove stops the interface and deletes wglink's files for it.
func (n Node) Remove(ctx context.Context, iface string) error {
	if err := checkIfaces([]string{iface}); err != nil {
		return err
	}
	_, err := n.run(ctx, "remove", map[string]string{"WG_IFACE": iface})
	return err
}

// ValidKey reports whether s is a base64 WireGuard key.
func ValidKey(s string) bool {
	return len(s) == 44 && strings.HasSuffix(s, "=") && strings.Trim(s[:43], "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/") == ""
}

// directive is one "@key arg" line from a script, with any "|" lines after it.
type directive struct {
	key, arg string
	raw      []byte
}

// parse splits script output into directives.
func parse(out []byte) []directive {
	var ds []directive
	for _, line := range strings.SplitAfter(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "@"):
			key, arg, _ := strings.Cut(strings.TrimRight(line[1:], "\n"), " ")
			ds = append(ds, directive{key: key, arg: arg})
		case strings.HasPrefix(line, "|") && len(ds) > 0:
			d := &ds[len(ds)-1]
			d.raw = append(d.raw, line[1:]...)
		}
	}
	return ds
}

// parseAddr accepts "a/b" or a bare address as a host prefix.
func parseAddr(s string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p, true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}
