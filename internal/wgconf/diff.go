package wgconf

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Change says how a new config can be loaded.
type Change int

const (
	Same    Change = iota // nothing to do
	Live                  // peers changed: wg syncconf, no dropped tunnels
	Restart               // interface settings or routes changed
)

func (c Change) String() string {
	return [...]string{"same", "live", "restart"}[c]
}

// Compare describes what changes from old to new and how to load it.
func Compare(old, new Interface) (Change, []string) {
	var why []string
	change := Same
	bump := func(c Change, format string, args ...any) {
		change = max(change, c)
		why = append(why, fmt.Sprintf(format, args...))
	}

	if !samePrefixes(old.Addresses, new.Addresses) {
		bump(Restart, "address %s -> %s", joinPrefixes(old.Addresses), joinPrefixes(new.Addresses))
	}
	if old.ListenPort != new.ListenPort {
		bump(Restart, "port %d -> %d", old.ListenPort, new.ListenPort)
	}
	if old.MTU != new.MTU {
		bump(Restart, "mtu %s -> %s", orAuto(old.MTU), orAuto(new.MTU))
	}
	if old.Forward != new.Forward {
		bump(Restart, "forwarding %s", onOff(new.Forward))
	}

	oldPeers := peerMap(old.Peers)
	newPeers := peerMap(new.Peers)
	for _, name := range sortedNames(newPeers) {
		n := newPeers[name]
		o, ok := oldPeers[name]
		if !ok {
			bump(Live, "add peer %s", name)
			continue
		}
		if o.PublicKey != n.PublicKey {
			bump(Live, "peer %s: new key", name)
		}
		if o.PresharedKey != n.PresharedKey {
			bump(Live, "peer %s: preshared key %s", name, onOff(n.PresharedKey))
		}
		if !samePrefixes(o.AllowedIPs, n.AllowedIPs) {
			bump(Live, "peer %s: allowed IPs %s -> %s", name, joinPrefixes(o.AllowedIPs), joinPrefixes(n.AllowedIPs))
		}
		if o.Endpoint != n.Endpoint {
			bump(Live, "peer %s: endpoint %s -> %s", name, orNone(o.Endpoint), orNone(n.Endpoint))
		}
		if o.Keepalive != n.Keepalive {
			bump(Live, "peer %s: keepalive %d -> %d", name, o.Keepalive, n.Keepalive)
		}
	}
	for _, name := range sortedNames(oldPeers) {
		if _, ok := newPeers[name]; !ok {
			bump(Live, "remove peer %s", name)
		}
	}
	// wg-quick adds routes only at start, so new routes need a restart.
	if change == Live && !samePrefixes(routed(old), routed(new)) {
		change = Restart
		why = append(why, "routes changed")
	}
	return change, why
}

// routed returns allowed IPs not already covered by the interface addresses.
func routed(i Interface) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range i.Peers {
		for _, a := range p.AllowedIPs {
			covered := false
			for _, own := range i.Addresses {
				if own.Masked().Bits() <= a.Bits() && own.Masked().Contains(a.Addr()) {
					covered = true
					break
				}
			}
			if !covered {
				out = append(out, a)
			}
		}
	}
	return out
}

// peerMap keys peers by name, falling back to the public key for unnamed ones.
func peerMap(peers []Peer) map[string]Peer {
	m := make(map[string]Peer, len(peers))
	for _, p := range peers {
		key := p.Name
		if key == "" {
			key = p.PublicKey
		}
		m[key] = p
	}
	return m
}

func sortedNames(m map[string]Peer) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// samePrefixes compares as sets.
func samePrefixes(a, b []netip.Prefix) bool {
	key := func(ps []netip.Prefix) string {
		s := make([]string, len(ps))
		for i, p := range ps {
			s[i] = p.String()
		}
		slices.Sort(s)
		return strings.Join(slices.Compact(s), ",")
	}
	return key(a) == key(b)
}

func orAuto(n int) string {
	if n == 0 {
		return "auto"
	}
	return fmt.Sprint(n)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
