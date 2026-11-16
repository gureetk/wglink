package engine

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/ipam"
	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

// Action is what apply will do to one interface.
type Action int

const (
	Keep    Action = iota // already right
	Create                // new interface
	Live                  // peers change; loaded with wg syncconf
	Restart               // interface settings change
	Start                 // config is right but the interface is down
	Skip                  // unreachable, or left out with --node
)

// Options narrow a plan.
type Options struct {
	Networks []string // only these networks; all when empty
	Nodes    []string // only change these nodes; all when empty
	Adopt    bool     // replace configs wglink didn't write
}

// Plan is the difference between wglink.yaml and the nodes.
type Plan struct {
	Networks []*NetPlan
	Removals []*Removal
	Problems []string // apply refuses while any exist
	Warnings []string

	opts     Options
	facts    map[string]*node.Facts
	assigned map[string]*ipam.Assigned
	keys     map[string]map[string]string // network -> node -> public key
}

// NetPlan is one network's plan.
type NetPlan struct {
	Name    string
	Iface   string
	Subnet  netip.Prefix
	Port    uint16
	Members []*MemberPlan
}

// MemberPlan is one node's interface in a network.
type MemberPlan struct {
	Node    string
	Addr    netip.Addr
	Action  Action
	Reasons []string
	NewKey  bool // apply creates the node's key first
	Drift   bool // the node's config changed since the last apply
}

// Removal is an interface wglink wrote that should no longer exist.
type Removal struct {
	Node    string
	Network string
	Iface   string
	Reason  string
	Gone    bool // the node left wglink.yaml; only state can be cleaned up
}

// Changes counts planned actions.
func (p *Plan) Changes() (create, live, restart, remove int) {
	for _, n := range p.Networks {
		for _, m := range n.Members {
			switch m.Action {
			case Create:
				create++
			case Live:
				live++
			case Restart, Start:
				restart++
			}
		}
	}
	for _, r := range p.Removals {
		if !r.Gone {
			remove++
		}
	}
	return
}

// Empty reports whether applying would change nothing on any node.
func (p *Plan) Empty() bool {
	c, l, r, d := p.Changes()
	return c+l+r+d == 0
}

func (p *Plan) problem(format string, args ...any) {
	p.Problems = append(p.Problems, fmt.Sprintf(format, args...))
}

func (p *Plan) warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// Plan probes the nodes involved and works out what apply would change.
func (s *Session) Plan(ctx context.Context, opts Options) (*Plan, error) {
	p := &Plan{opts: opts, facts: map[string]*node.Facts{}, keys: map[string]map[string]string{}}
	scoped, err := s.scope(opts.Networks)
	if err != nil {
		return nil, err
	}
	for _, name := range opts.Nodes {
		if _, ok := s.Inv.Nodes[name]; !ok {
			return nil, fmt.Errorf("no node named %s", name)
		}
	}

	// Ask each node about the interfaces it has, or should have.
	ifaces := map[string]map[string]bool{}
	want := func(nodeName, iface string) {
		if ifaces[nodeName] == nil {
			ifaces[nodeName] = map[string]bool{}
		}
		ifaces[nodeName][iface] = true
	}
	for _, name := range scoped {
		if n, ok := s.Inv.Networks[name]; ok {
			for _, m := range n.Members {
				want(m.Name, n.IfaceName())
			}
		}
		if sn, ok := s.State.Networks[name]; ok {
			for _, m := range sn.MemberNames() {
				if _, ok := s.Inv.Nodes[m]; !ok {
					p.Removals = append(p.Removals, &Removal{Node: m, Network: name, Iface: stateIface(name, sn.Iface), Reason: "node removed from wglink.yaml", Gone: true})
					p.warn("%s is no longer in wglink.yaml; remove %s on it by hand", m, stateIface(name, sn.Iface))
					continue
				}
				want(m, stateIface(name, sn.Iface))
			}
		}
	}
	var mu sync.Mutex
	probeErrs := s.each(ctx, keys(ifaces), func(ctx context.Context, name string) error {
		nd, err := s.Node(name)
		if err != nil {
			return err
		}
		f, err := nd.Probe(ctx, keys(ifaces[name]))
		if err != nil {
			return err
		}
		mu.Lock()
		p.facts[name] = f
		mu.Unlock()
		return nil
	})
	for _, name := range keys(probeErrs) {
		if p.inNodeScope(name) {
			p.problem("%v", probeErrs[name])
		} else {
			p.warn("%v", probeErrs[name])
		}
	}

	// Endpoints for members of networks being planned.
	members := map[string]bool{}
	for _, name := range scoped {
		if n, ok := s.Inv.Networks[name]; ok {
			for _, m := range n.Members {
				members[m.Name] = true
			}
		}
	}
	for name, err := range s.each(ctx, keys(members), func(ctx context.Context, name string) error {
		_, err := s.Endpoint(ctx, name)
		return err
	}) {
		p.problem("%s: endpoint: %v", name, err)
	}

	assigned, err := ipam.Resolve(s.ipamRequest(p.facts))
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			p.problem("%s", line)
		}
		return p, nil
	}
	p.assigned = assigned

	for _, name := range scoped {
		if n, ok := s.Inv.Networks[name]; ok {
			p.Networks = append(p.Networks, s.planNetwork(ctx, p, n))
		}
	}
	s.planRemovals(p, scoped)
	s.checkNodes(p, members)
	return p, nil
}

// scope lists the networks to plan: the named ones, or every network in
// wglink.yaml and every one recorded in state.
func (s *Session) scope(names []string) ([]string, error) {
	if len(names) > 0 {
		for _, n := range names {
			_, inInv := s.Inv.Networks[n]
			_, inState := s.State.Networks[n]
			if !inInv && !inState {
				return nil, fmt.Errorf("no network named %s", n)
			}
		}
		out := slices.Clone(names)
		sort.Strings(out)
		return slices.Compact(out), nil
	}
	all := map[string]bool{}
	for n := range s.Inv.Networks {
		all[n] = true
	}
	for n := range s.State.Networks {
		all[n] = true
	}
	return keys(all), nil
}

// willKey reports whether this run can create a key on the node.
func (p *Plan) willKey(name string) bool { return p.facts[name] != nil && p.inNodeScope(name) }

func (p *Plan) inNodeScope(name string) bool {
	return len(p.opts.Nodes) == 0 || slices.Contains(p.opts.Nodes, name)
}

func (p *Plan) inNetScope(name string) bool {
	return len(p.opts.Networks) == 0 || slices.Contains(p.opts.Networks, name)
}

func stateIface(network, iface string) string {
	if iface != "" {
		return iface
	}
	return "wg-" + network
}

// ipamRequest gathers every network, what was recorded and what each probed node already uses.
func (s *Session) ipamRequest(facts map[string]*node.Facts) ipam.Request {
	req := ipam.Request{
		Pool:     s.Inv.Defaults.Pool,
		Prefix:   s.Inv.Defaults.Prefix,
		BasePort: uint16(s.Inv.Defaults.Port),
		Used:     map[string][]ipam.Use{},
		Ports:    map[string][]uint16{},
	}
	ours := map[string]bool{}
	for _, n := range s.Inv.Networks {
		ours[n.IfaceName()] = true
	}
	for _, name := range s.Inv.NetworkNames() {
		n := s.Inv.Networks[name]
		net := ipam.Net{Name: name, Subnet: n.Subnet.Prefix, Port: uint16(n.Port)}
		for _, m := range n.Members {
			net.Members = append(net.Members, ipam.Member{Node: m.Name, Addr: m.Addr})
		}
		if sn, ok := s.State.Networks[name]; ok {
			net.Prev = ipam.Assigned{Subnet: sn.Subnet, Port: sn.Port, Addrs: sn.Addrs()}
		}
		req.Networks = append(req.Networks, net)
	}
	for name, f := range facts {
		req.Used[name], req.Ports[name] = used(f, ours)
	}
	return req
}

// used lists a node's addresses, routes and UDP ports, leaving out wglink's own interfaces.
func used(f *node.Facts, ours map[string]bool) ([]ipam.Use, []uint16) {
	skip := func(iface string) bool { return ours[iface] || f.Managed[iface] != "" }
	var out []ipam.Use
	for _, a := range f.Addrs {
		if !skip(a.Iface) && !ignorable(a.Prefix) {
			out = append(out, ipam.Use{Prefix: a.Prefix.Masked(), What: "address on " + a.Iface})
		}
	}
	for _, r := range f.Routes {
		if skip(r.Dev) || ignorable(r.Prefix) {
			continue
		}
		what := "route"
		if r.Dev != "" {
			what += " via " + r.Dev
		}
		if r.Table != "" && r.Table != "main" {
			what += " in table " + r.Table
		}
		out = append(out, ipam.Use{Prefix: r.Prefix, What: what})
	}
	ownPorts := map[uint16]bool{}
	for iface, port := range f.WGPorts {
		if skip(iface) {
			ownPorts[port] = true
		}
	}
	var ports []uint16
	for _, port := range f.UDPPorts {
		if !ownPorts[port] {
			ports = append(ports, port)
		}
	}
	return out, ports
}

func ignorable(p netip.Prefix) bool {
	a := p.Addr()
	return p.Bits() == 0 || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsLinkLocalMulticast()
}

// planNetwork compares each member's interface with what it should be.
func (s *Session) planNetwork(ctx context.Context, p *Plan, n *config.Network) *NetPlan {
	a := p.assigned[n.Name]
	np := &NetPlan{Name: n.Name, Iface: n.IfaceName(), Subnet: a.Subnet, Port: a.Port}
	p.keys[n.Name] = map[string]string{}
	sn := s.State.Networks[n.Name]
	for _, m := range n.Members {
		key := ""
		if f := p.facts[m.Name]; f != nil {
			if i := f.Ifaces[np.Iface]; i != nil {
				key = i.PubKey
			}
		} else if sn != nil && sn.Members[m.Name] != nil {
			key = sn.Members[m.Name].PubKey
		}
		p.keys[n.Name][m.Name] = key
	}
	if len(n.Members) < 2 {
		p.warn("network %s has fewer than two members, so there is nothing to connect yet", n.Name)
	}
	for _, m := range n.Members {
		if p.keys[n.Name][m.Name] == "" && !p.willKey(m.Name) {
			p.warn("network %s: %s has no key yet, so its peers leave it out until it is applied too", n.Name, m.Name)
		}
	}
	s.warnUnreachablePairs(ctx, p, n)

	for _, m := range n.Members {
		mp := &MemberPlan{Node: m.Name, Addr: a.Addrs[m.Name]}
		np.Members = append(np.Members, mp)
		f := p.facts[m.Name]
		switch {
		case f == nil:
			mp.Action, mp.Reasons = Skip, []string{"unreachable"}
			continue
		case !p.inNodeScope(m.Name):
			mp.Action, mp.Reasons = Skip, []string{"not selected"}
			continue
		}
		mp.NewKey = p.keys[n.Name][m.Name] == ""
		want := s.desired(ctx, p, n, m.Name)
		iface := f.Ifaces[np.Iface]
		if iface == nil {
			iface = &node.Iface{}
		}
		switch iface.Conf {
		case node.ConfNone:
			mp.Action = Create
			mp.Reasons = []string{fmt.Sprintf("new interface, %d %s", len(want.Peers), plural(len(want.Peers), "peer", "peers"))}
		case node.ConfUnmanaged:
			if !p.opts.Adopt {
				p.problem("%s: %s.conf exists and wasn't written by wglink; rerun with --adopt to replace it", m.Name, np.Iface)
			}
			mp.Action, mp.Reasons = Restart, []string{"replace a config wglink didn't write"}
		case node.ConfManaged:
			s.compare(mp, iface, want, sn)
		}
		if mp.NewKey {
			mp.Reasons = append(mp.Reasons, "new key")
			if mp.Action == Keep {
				mp.Action = Live
			}
		}
	}
	return np
}

// compare classifies an existing managed config against the desired one.
func (s *Session) compare(mp *MemberPlan, iface *node.Iface, want wgconf.Interface, sn *state.Network) {
	if sn != nil {
		if sm := sn.Members[mp.Node]; sm != nil && sm.Applied != "" && sm.Applied != wgconf.Hash(iface.Config) {
			mp.Drift = true
		}
	}
	have, err := wgconf.Parse(iface.Config)
	if err != nil {
		mp.Action, mp.Reasons = Restart, []string{"unreadable config: " + err.Error()}
		return
	}
	change, why := wgconf.Compare(have, want)
	mp.Reasons = why
	switch change {
	case wgconf.Same:
		mp.Action = Keep
		if !iface.Up {
			mp.Action, mp.Reasons = Start, []string{"interface is down"}
		} else if wgconf.Hash(iface.Config) != wgconf.Hash(wgconf.Render(want)) {
			// Same settings, different text: put the file back as wglink writes it.
			mp.Action, mp.Reasons = Live, []string{"undo edits made on the node"}
		}
	case wgconf.Live:
		mp.Action = Live
	case wgconf.Restart:
		mp.Action = Restart
	}
	if mp.Drift {
		mp.Reasons = append([]string{"edited on the node since the last apply"}, mp.Reasons...)
	}
}

// desired renders what member's interface should look like. Peers without
// a key yet get a placeholder; apply renders again once keys exist.
func (s *Session) desired(ctx context.Context, p *Plan, n *config.Network, member string) wgconf.Interface {
	a := p.assigned[n.Name]
	i := wgconf.Interface{
		Network:    n.Name,
		Addresses:  []netip.Prefix{netip.PrefixFrom(a.Addrs[member], a.Subnet.Bits())},
		ListenPort: a.Port,
		MTU:        n.MTU,
	}
	keepalive := n.Keepalive
	if k := s.Inv.Nodes[member].Keepalive; k != 0 {
		keepalive = k
	}
	for _, peer := range n.Members {
		if peer.Name == member {
			continue
		}
		key := p.keys[n.Name][peer.Name]
		if key == "" {
			if !p.willKey(peer.Name) {
				continue // no key yet and none coming in this run
			}
			key = "(new key for " + peer.Name + ")"
		}
		endpoint := ""
		if e, err := s.Endpoint(ctx, peer.Name); err == nil && !e.None {
			endpoint = e.Dial(a.Port)
		}
		i.Peers = append(i.Peers, wgconf.Peer{
			Name:       peer.Name,
			PublicKey:  key,
			AllowedIPs: []netip.Prefix{netip.PrefixFrom(a.Addrs[peer.Name], 32)},
			Endpoint:   endpoint,
			Keepalive:  keepalive,
		})
	}
	return i
}

func (s *Session) warnUnreachablePairs(ctx context.Context, p *Plan, n *config.Network) {
	dialable := func(name string) bool {
		e, err := s.Endpoint(ctx, name)
		return err == nil && !e.None
	}
	for i, a := range n.Members {
		for _, b := range n.Members[i+1:] {
			if !dialable(a.Name) && !dialable(b.Name) {
				p.warn("network %s: neither %s nor %s has an endpoint, so they can't reach each other", n.Name, a.Name, b.Name)
			}
		}
	}
}

// planRemovals finds interfaces wglink wrote that are no longer wanted.
func (s *Session) planRemovals(p *Plan, scoped []string) {
	for _, nodeName := range keys(p.facts) {
		if !p.inNodeScope(nodeName) {
			continue
		}
		f := p.facts[nodeName]
		for _, iface := range keys(f.Managed) {
			network := f.Managed[iface]
			if !p.inNetScope(network) {
				continue
			}
			n, ok := s.Inv.Networks[network]
			reason := "network removed from wglink.yaml"
			if ok {
				if _, member := n.Member(nodeName); member && n.IfaceName() == iface {
					continue
				}
				reason = "no longer a member of " + network
				if _, member := n.Member(nodeName); member {
					reason = "interface renamed to " + n.IfaceName()
				}
			}
			p.Removals = append(p.Removals, &Removal{Node: nodeName, Network: network, Iface: iface, Reason: reason})
		}
	}
	sort.SliceStable(p.Removals, func(i, j int) bool {
		a, b := p.Removals[i], p.Removals[j]
		return a.Node+a.Iface < b.Node+b.Iface
	})
}

// checkNodes reports missing tools, missing privileges and other trouble.
func (s *Session) checkNodes(p *Plan, members map[string]bool) {
	for _, name := range keys(members) {
		f := p.facts[name]
		if f == nil || !p.inNodeScope(name) {
			continue
		}
		n := s.Inv.Nodes[name]
		if !f.Have["wg"] || !f.Have["wg-quick"] {
			p.problem("%s: wireguard-tools is not installed (%s)", name, InstallHint(f.OS, "wireguard-tools"))
		}
		if !f.Have["ip"] {
			p.problem("%s: iproute2 is not installed (%s)", name, InstallHint(f.OS, "iproute2"))
		}
		if f.UID != 0 {
			p.problem("%s: wglink needs root there; connect as root or set sudo: true", name)
		}
		if !f.Systemd && n.Service != "none" {
			p.warn("%s has no systemd, so its tunnels won't come back after a reboot", name)
		}
		if n.Reach() == config.ReachPCT && !f.Module {
			p.warn("%s can't see the wireguard kernel module; if its tunnel fails to start, run \"modprobe wireguard\" on %s and add wireguard to /etc/modules-load.d/", name, n.PCT.Via)
		}
	}
}

// InstallHint is the command that installs pkg on the given OS ID.
func InstallHint(osID, pkg string) string {
	switch osID {
	case "debian", "ubuntu", "raspbian", "linuxmint", "pop":
		return "apt install " + pkg
	case "fedora", "rhel", "centos", "rocky", "almalinux":
		return "dnf install " + pkg
	case "alpine":
		return "apk add " + pkg
	case "arch", "manjaro":
		return "pacman -S " + pkg
	case "opensuse-leap", "opensuse-tumbleweed", "sles":
		return "zypper install " + pkg
	}
	return "install " + pkg
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
