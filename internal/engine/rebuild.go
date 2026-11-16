package engine

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

// Rebuilt is what Rebuild recovered for one network.
type Rebuilt struct {
	Name    string
	Iface   string
	Subnet  netip.Prefix // zero if no member had the network's config
	Port    uint16
	Members []RebuiltMember
	Notes   []string // disagreements between members' configs
}

// RebuiltMember is one member's recovered address, and where it came from.
type RebuiltMember struct {
	Node string
	Addr netip.Addr // zero if not found
	From string     // "its config", "its peers' configs", or why it wasn't found
}

// Rebuild puts the state file back together from the configs on the nodes,
// for when it was lost or went out of date. A member wglink can't read
// directly, such as a manual or unreachable one, is taken from its peers'
// configs. Manual nodes are assumed to have their current config.
func (s *Session) Rebuild(ctx context.Context, networks []string) ([]*Rebuilt, error) {
	names := networks
	if len(names) == 0 {
		names = s.Inv.NetworkNames()
	}
	ifaces := map[string][]string{}
	for _, name := range names {
		n, ok := s.Inv.Networks[name]
		if !ok {
			return nil, fmt.Errorf("no network named %s", name)
		}
		for _, m := range n.Members {
			if !s.manual(m.Name) {
				ifaces[m.Name] = append(ifaces[m.Name], n.IfaceName())
			}
		}
	}
	var mu sync.Mutex
	facts := map[string]*node.Facts{}
	errs := s.each(ctx, keys(ifaces), func(ctx context.Context, name string) error {
		nd, err := s.Node(name)
		if err != nil {
			return err
		}
		f, err := nd.Probe(ctx, ifaces[name])
		if err != nil {
			return err
		}
		mu.Lock()
		facts[name] = f
		mu.Unlock()
		return nil
	})

	var out []*Rebuilt
	var manual [][2]string
	unlock, err := state.Lock(s.Dir)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		n := s.Inv.Networks[name]
		r, sn := rebuildNetwork(n, facts, errs, s.manual)
		out = append(out, r)
		if sn == nil {
			delete(s.State.Networks, name)
			continue
		}
		s.State.Networks[name] = sn
		for _, m := range n.Members {
			if s.manual(m.Name) && sn.Members[m.Name] != nil && s.Inv.Nodes[m.Name].Manual.PubKey != "" {
				manual = append(manual, [2]string{name, m.Name})
			}
		}
	}
	err = s.SaveState()
	unlock()
	if err != nil {
		return nil, err
	}
	// Record each manual node's config as given, with the preshared keys its
	// peers have, so plan doesn't ask for exports.
	for _, nm := range manual {
		if _, err := s.export(ctx, nm[0], nm[1], false); err != nil {
			return out, fmt.Errorf("%s: %w", nm[1], err)
		}
	}
	return out, nil
}

// rebuildNetwork reads one network's state out of its members' configs.
func rebuildNetwork(n *config.Network, facts map[string]*node.Facts, errs map[string]error, manual func(string) bool) (*Rebuilt, *state.Network) {
	iface := n.IfaceName()
	r := &Rebuilt{Name: n.Name, Iface: iface}
	sn := &state.Network{Iface: iface, Members: map[string]*state.Member{}}
	configs := map[string]wgconf.Interface{}
	for _, m := range n.Members {
		f := facts[m.Name]
		if f == nil || f.Managed[iface] != n.Name || f.Ifaces[iface] == nil {
			continue
		}
		c, err := wgconf.Parse(f.Ifaces[iface].Config)
		if err != nil || len(c.Addresses) == 0 {
			continue
		}
		configs[m.Name] = c
		subnet, port := c.Addresses[0].Masked(), c.ListenPort
		switch {
		case !sn.Subnet.IsValid():
			sn.Subnet, sn.Port = subnet, port
		case subnet != sn.Subnet || port != sn.Port:
			r.Notes = append(r.Notes, fmt.Sprintf("%s has %s udp/%d, unlike the others; plan will change it", m.Name, subnet, port))
		}
		sn.Members[m.Name] = &state.Member{IP: c.Addresses[0].Addr(), PubKey: f.Ifaces[iface].PubKey, Applied: wgconf.Hash(f.Ifaces[iface].Config)}
	}
	if len(configs) == 0 {
		return r, nil
	}
	r.Subnet, r.Port = sn.Subnet, sn.Port
	for _, m := range n.Members {
		rm := RebuiltMember{Node: m.Name}
		switch {
		case sn.Members[m.Name] != nil:
			rm.Addr, rm.From = sn.Members[m.Name].IP, "its config"
		default:
			if sm := fromPeers(m.Name, configs, sn.Subnet); sm != nil {
				sn.Members[m.Name] = sm
				rm.Addr, rm.From = sm.IP, "its peers' configs"
			} else {
				rm.From = "not found"
			}
			switch {
			case manual(m.Name):
				rm.From += " (manual node)"
			case errs[m.Name] != nil:
				rm.From += " (unreachable)"
			default:
				rm.From += " (it has no " + iface + ")"
			}
		}
		r.Members = append(r.Members, rm)
	}
	return r, sn
}

// fromPeers finds a member in its peers' configs by the name wglink writes
// above each peer.
func fromPeers(name string, configs map[string]wgconf.Interface, subnet netip.Prefix) *state.Member {
	for _, owner := range keys(configs) {
		for _, p := range configs[owner].Peers {
			if p.Name != name {
				continue
			}
			for _, a := range p.AllowedIPs {
				if a.IsSingleIP() && subnet.Contains(a.Addr()) {
					return &state.Member{IP: a.Addr(), PubKey: p.PublicKey}
				}
			}
		}
	}
	return nil
}
