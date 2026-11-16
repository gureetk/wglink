package config

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

var (
	nodeNameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	netNameRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,11}$`)
	ifaceNameRE = regexp.MustCompile(`^[a-zA-Z0-9_=+.-]{1,15}$`)
)

// ValidNodeName reports whether s can name a node.
func ValidNodeName(s string) bool { return nodeNameRE.MatchString(s) }

// ValidNetworkName reports whether s can name a network.
func ValidNetworkName(s string) bool { return netNameRE.MatchString(s) }

// Validate checks the whole inventory and returns every problem found.
func (inv *Inventory) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if inv.Version != 1 {
		add("version: want 1, got %d", inv.Version)
	}
	d := inv.Defaults
	switch {
	case !d.Pool.Addr().Is4() || d.Pool != d.Pool.Masked():
		add("defaults.pool: %s must be an IPv4 network address like 10.99.0.0/16", d.Pool)
	case d.Prefix < d.Pool.Bits() || d.Prefix > 30:
		add("defaults.prefix: /%d must be between /%d (the pool) and /30", d.Prefix, d.Pool.Bits())
	}
	if d.Port < 1 || d.Port > 65535 {
		add("defaults.port: %d must be 1-65535", d.Port)
	}

	members := map[string]int{}
	for _, name := range inv.NetworkNames() {
		for _, m := range inv.Networks[name].Members {
			members[m.Name]++
		}
	}
	for _, name := range inv.NodeNames() {
		errs = append(errs, inv.validateNode(inv.Nodes[name], members[name] > 0)...)
	}

	ifaces := map[string]string{}
	for _, name := range inv.NetworkNames() {
		n := inv.Networks[name]
		errs = append(errs, inv.validateNetwork(n)...)
		if other, ok := ifaces[n.IfaceName()]; ok {
			add("network %s: interface %s is also used by network %s", name, n.IfaceName(), other)
		}
		ifaces[n.IfaceName()] = name
	}
	return errors.Join(errs...)
}

func (inv *Inventory) validateNode(n *Node, isMember bool) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("node %s: "+format, append([]any{n.Name}, args...)...))
	}
	if !ValidNodeName(n.Name) {
		add("name must be lowercase letters, digits and hyphens, starting with a letter, at most 32 long")
	}
	switch n.Reach() {
	case "":
		add("set exactly one of ssh, local, exec or pct")
	case ReachSSH:
		if err := checkSSHDest(n.SSH); err != nil {
			add("ssh: %v", err)
		}
	case ReachExec:
		if err := checkArgv(n.Exec); err != nil {
			add("exec: %v", err)
		}
	case ReachPCT:
		errs = append(errs, inv.checkGuest(n, "pct", n.PCT)...)
	}
	if n.Sudo && n.Reach() != ReachSSH && n.Reach() != ReachLocal {
		add("sudo only applies to ssh and local nodes")
	}
	if n.Keepalive < 0 || n.Keepalive > 65535 {
		add("keepalive: %d must be 0-65535 seconds", n.Keepalive)
	}
	switch n.Service {
	case "", "auto", "systemd", "none":
	default:
		add("service: %q must be auto, systemd or none", n.Service)
	}
	if isMember && n.Endpoint == nil && n.Reach() != ReachSSH && n.Reach() != "" {
		add("endpoint: required for %s nodes in a network (or \"none\" if peers can't reach it)", n.Reach())
	}
	return errs
}

func (inv *Inventory) checkGuest(n *Node, kind string, g *GuestRef) []error {
	var errs []error
	host, ok := inv.Nodes[g.Via]
	switch {
	case g.Via == "":
		errs = append(errs, fmt.Errorf("node %s: %s.via: name the Proxmox host node", n.Name, kind))
	case !ok:
		errs = append(errs, fmt.Errorf("node %s: %s.via: no node named %q", n.Name, kind, g.Via))
	case host.Reach() != ReachSSH && host.Reach() != ReachLocal && host.Reach() != ReachExec:
		errs = append(errs, fmt.Errorf("node %s: %s.via: %s must be an ssh, local or exec node", n.Name, kind, g.Via))
	}
	if g.VMID < 100 || g.VMID > 999999999 {
		errs = append(errs, fmt.Errorf("node %s: %s.vmid: %d must be 100-999999999", n.Name, kind, g.VMID))
	}
	return errs
}

func (inv *Inventory) validateNetwork(n *Network) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("network %s: "+format, append([]any{n.Name}, args...)...))
	}
	if !ValidNetworkName(n.Name) {
		add("name must be lowercase letters, digits and hyphens, starting with a letter, at most 12 long")
	}
	if !ifaceNameRE.MatchString(n.IfaceName()) {
		add("iface: %q must be 1-15 letters, digits or _=+.-", n.IfaceName())
	}
	if n.Port < 0 || n.Port > 65535 {
		add("port: %d must be 1-65535", n.Port)
	}
	if n.MTU != 0 && (n.MTU < 1280 || n.MTU > 9000) {
		add("mtu: %d must be 1280-9000", n.MTU)
	}
	if n.Keepalive < 0 || n.Keepalive > 65535 {
		add("keepalive: %d must be 0-65535 seconds", n.Keepalive)
	}
	seenNode := map[string]bool{}
	seenAddr := map[netip.Addr]string{}
	for _, m := range n.Members {
		if _, ok := inv.Nodes[m.Name]; !ok {
			add("member %s: no node with that name", m.Name)
			continue
		}
		if seenNode[m.Name] {
			add("member %s: listed twice", m.Name)
		}
		seenNode[m.Name] = true
		if m.Addr.Kind == AddrAuto {
			continue
		}
		if n.Subnet.Auto() {
			if m.Addr.Kind == AddrFixed {
				add("member %s: a full address needs a fixed subnet; set subnet or use a host part like .10", m.Name)
			}
			continue
		}
		a, err := m.Addr.In(n.Subnet.Prefix)
		if err != nil {
			add("member %s: %v", m.Name, err)
			continue
		}
		if other, ok := seenAddr[a]; ok {
			add("member %s: address %s is also pinned for %s", m.Name, a, other)
		}
		seenAddr[a] = m.Name
	}
	return errs
}

// checkSSHDest rejects destinations ssh could read as options or that hold odd bytes.
func checkSSHDest(s string) error {
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%q must not start with -", s)
	}
	for _, c := range s {
		if c <= ' ' || c > '~' {
			return fmt.Errorf("%q contains spaces or unprintable characters", s)
		}
	}
	return nil
}

// checkArgv validates a command prefix run without a shell.
func checkArgv(argv []string) error {
	if len(argv) == 0 {
		return errors.New("give a command, e.g. [docker, exec, -i, web]")
	}
	if strings.HasPrefix(argv[0], "-") {
		return fmt.Errorf("command %q must not start with -", argv[0])
	}
	for _, a := range argv {
		if a == "" || strings.ContainsRune(a, 0) {
			return errors.New("arguments must be non-empty")
		}
	}
	return nil
}
