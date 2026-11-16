package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

// pair is two members of a network, in order.
type pair [2]string

func pairOf(a, b string) pair {
	if a > b {
		a, b = b, a
	}
	return pair{a, b}
}

// pskState is whether a pair of members shares a preshared key once apply
// is done.
type pskState int

const (
	pskNone pskState = iota
	pskKeep          // they keep the one they have
	pskNew           // apply makes one and gives it to both
)

// planPSKs works out, in a network with preshared keys, which linked pairs
// share one after apply, and which need a new one. Keys are compared by the
// hashes nodes report; manual nodes are taken to have the keys their last
// export gave them.
func (s *Session) planPSKs(p *Plan, n *config.Network, sn *state.Network) {
	psks := map[pair]pskState{}
	p.psks[n.Name] = psks
	if !n.PSK {
		return
	}
	for i, ma := range n.Members {
		for _, mb := range n.Members[i+1:] {
			a, b := ma.Name, mb.Name
			if !n.Linked(a, b) || s.manual(a) && s.manual(b) {
				continue // two devices can't be given a key
			}
			if s.manual(a) {
				a, b = b, a
			}
			fa, fb := p.pskFile(n, a, b), p.pskFile(n, b, a)
			st := pskNone
			switch {
			case s.manual(b):
				if h := given(sn, b, a); h != "" && h == fa {
					st = pskKeep
				}
			case p.applies(a) && p.applies(b):
				st = pskNew
				if fa != "" && fa == fb && !p.rotating(a) && !p.rotating(b) {
					st = pskKeep
				}
			default:
				// Only one side changes, so it keeps the key it has.
				if p.applies(a) && fa != "" || p.applies(b) && fb != "" {
					st = pskKeep
				}
				if p.facts[a] != nil && p.facts[b] != nil && fa != fb {
					p.warn("network %s: %s and %s have different preshared keys; apply both to replace them", n.Name, a, b)
				}
			}
			psks[pairOf(a, b)] = st
		}
	}
}

// given is the hash of the preshared key a manual node's last export gave
// it for peer.
func given(sn *state.Network, member, peer string) string {
	if sn == nil || sn.Members[member] == nil {
		return ""
	}
	return sn.Members[member].PSKs[peer]
}

// pskFile is the hash of the preshared key member has stored for peer.
func (p *Plan) pskFile(n *config.Network, member, peer string) string {
	if f := p.facts[member]; f != nil {
		if i := f.Ifaces[n.IfaceName()]; i != nil {
			return i.PSKFile[peer]
		}
	}
	return ""
}

// pskOn reports whether member's config has a preshared key for peer. A
// manual node gets one for each peer wglink can reach when it's exported.
func (s *Session) pskOn(p *Plan, n *config.Network, member, peer string) bool {
	switch {
	case !n.PSK || s.manual(member) && s.manual(peer):
		return false
	case s.manual(member):
		return true
	}
	return p.psks[n.Name][pairOf(member, peer)] != pskNone
}

// pskReasons explains a member's preshared key changes: new keys, and keys
// stored since its config was written.
func (s *Session) pskReasons(p *Plan, n *config.Network, member string, iface *node.Iface) []string {
	var fresh, load []string
	for _, peer := range n.Members {
		switch p.psks[n.Name][pairOf(member, peer.Name)] {
		case pskNew:
			fresh = append(fresh, peer.Name)
		case pskKeep:
			if c := iface.PSKConf[peer.Name]; c != "" && c != iface.PSKFile[peer.Name] {
				load = append(load, peer.Name)
			}
		}
	}
	var why []string
	if len(fresh) > 0 {
		why = append(why, fmt.Sprintf("new preshared %s with %s", plural(len(fresh), "key", "keys"), andList(fresh)))
	}
	if len(load) > 0 {
		why = append(why, fmt.Sprintf("load the preshared %s for %s", plural(len(load), "key", "keys"), andList(load)))
	}
	return why
}

// andList joins names as "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// changedPSKs lists the peers whose preshared key for a manual node is no
// longer the one its last export gave it.
func (s *Session) changedPSKs(p *Plan, n *config.Network, member string, sm *state.Member) []string {
	var out []string
	for _, peer := range n.Members {
		h := sm.PSKs[peer.Name]
		if h != "" && p.facts[peer.Name] != nil && p.pskFile(n, peer.Name, member) != h {
			out = append(out, peer.Name)
		}
	}
	return out
}

// storePSKs makes the new preshared keys a plan calls for and stores each
// on both members of its pair, before any config refers to it.
func (s *Session) storePSKs(ctx context.Context, p *Plan, np *NetPlan, logf Logf) error {
	var pairs []pair
	for pr, st := range p.psks[np.Name] {
		if st == pskNew {
			pairs = append(pairs, pr)
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int { return strings.Compare(a[0]+" "+a[1], b[0]+" "+b[1]) })
	give := map[string]map[string]string{}
	for _, pr := range pairs {
		key, err := wgconf.GeneratePSK()
		if err != nil {
			return err
		}
		for i, name := range pr {
			if give[name] == nil {
				give[name] = map[string]string{}
			}
			give[name][pr[1-i]] = key
		}
	}
	for _, name := range keys(give) {
		nd, err := s.Node(name)
		if err != nil {
			return err
		}
		if err := s.timed(ctx, func(ctx context.Context) error { return nd.SetPSKs(ctx, np.Iface, give[name]) }); err != nil {
			return fmt.Errorf("storing preshared keys on %s: %w", name, err)
		}
		logf("%s: stored %d new preshared %s for %s", name, len(give[name]), plural(len(give[name]), "key", "keys"), np.Iface)
	}
	return nil
}
