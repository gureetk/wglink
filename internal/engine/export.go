package engine

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

// Exported is a manual node's config, ready to load on the device once its
// private key is added.
type Exported struct {
	Config  []byte            // no PrivateKey line; preshared keys as "@peer" placeholders
	PSKs    map[string]string // the preshared keys, by peer
	Missing []string          // members left out because they have no key yet
	NoPSK   []string          // peers it has no preshared key for, as they couldn't be reached
	Plan    *Plan             // the plan it came from; not empty means apply is due
}

// ExportText is a manual node's config without its private key.
func ExportText(i wgconf.Interface, member string) []byte {
	text := wgconf.Render(i)
	if _, rest, ok := bytes.Cut(text, []byte("\n")); ok {
		text = rest
	}
	head := fmt.Sprintf("# Exported by wglink for %s (network %s). wglink can't update this copy;\n# export again when wglink plan says so.\n", member, i.Network)
	return append([]byte(head), text...)
}

// Export works out a manual member's config and records it as given, so
// plan can tell when it goes stale. The member's public key must already be
// in wglink.yaml. In a network with preshared keys, each peer it can reach
// stores a new one for the member, and Export returns them for the config.
func (s *Session) Export(ctx context.Context, network, member string) (*Exported, error) {
	return s.export(ctx, network, member, true)
}

// export is Export. Without fresh, it makes no preshared keys and takes the
// device to have the ones its peers have.
func (s *Session) export(ctx context.Context, network, member string, fresh bool) (*Exported, error) {
	n, ok := s.Inv.Networks[network]
	if !ok {
		return nil, fmt.Errorf("no network named %s", network)
	}
	if _, ok := n.Member(member); !ok {
		return nil, fmt.Errorf("%s isn't in network %s", member, network)
	}
	if !s.manual(member) {
		return nil, fmt.Errorf("%s isn't a manual node; wglink sets it up with apply", member)
	}
	pub := s.Inv.Nodes[member].Manual.PubKey
	if pub == "" {
		return nil, fmt.Errorf("%s has no public key yet", member)
	}
	p, err := s.Plan(ctx, Options{Networks: []string{network}})
	if err != nil {
		return nil, err
	}
	if p.assigned == nil {
		return &Exported{Plan: p}, fmt.Errorf("can't work out addresses for %s", network)
	}

	want := s.desired(ctx, p, n, member)
	var missing []string
	want.Peers = slices.DeleteFunc(want.Peers, func(peer wgconf.Peer) bool {
		if !wgconf.ValidKey(peer.PublicKey) {
			missing = append(missing, peer.Name)
			return true
		}
		return false
	})
	ex := &Exported{Missing: missing, Plan: p}
	given := map[string]string{} // hashes of the preshared keys, by peer
	for i, peer := range want.Peers {
		switch {
		case !peer.PresharedKey:
		case p.facts[peer.Name] == nil:
			want.Peers[i].PresharedKey = false
			ex.NoPSK = append(ex.NoPSK, peer.Name)
		case !fresh:
			if h := p.pskFile(n, peer.Name, member); h != "" {
				given[peer.Name] = h
			} else {
				want.Peers[i].PresharedKey = false
			}
		default:
			key, err := wgconf.GeneratePSK()
			if err != nil {
				return nil, err
			}
			if ex.PSKs == nil {
				ex.PSKs = map[string]string{}
			}
			ex.PSKs[peer.Name], given[peer.Name] = key, wgconf.PSKHash(key)
		}
	}
	text := ExportText(want, member)

	unlock, err := state.Lock(s.Dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	for _, peer := range keys(ex.PSKs) {
		nd, err := s.Node(peer)
		if err != nil {
			return nil, err
		}
		psk := map[string]string{member: ex.PSKs[peer]}
		if err := s.timed(ctx, func(ctx context.Context) error { return nd.SetPSKs(ctx, n.IfaceName(), psk) }); err != nil {
			return nil, fmt.Errorf("storing a preshared key for %s on %s: %w", member, peer, err)
		}
	}
	a := p.assigned[network]
	sn := s.State.Net(network)
	if !sn.Subnet.IsValid() {
		sn.Iface, sn.Subnet, sn.Port = n.IfaceName(), a.Subnet, a.Port
	}
	if !sn.Subnet6.IsValid() {
		sn.Subnet6 = a.Subnet6
	}
	sm := sn.Member(member)
	sm.IP, sm.PubKey, sm.Applied, sm.PSKs = a.Addrs[member], pub, wgconf.Hash(text), nil
	if len(given) > 0 {
		sm.PSKs = given
	}
	if err := s.SaveState(); err != nil {
		return nil, err
	}
	ex.Config = text
	return ex, nil
}
