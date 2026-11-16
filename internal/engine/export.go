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
	Config  []byte   // no PrivateKey line
	Missing []string // members left out because they have no key yet
	Plan    *Plan    // the plan it came from; not empty means apply is due
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
// in wglink.yaml.
func (s *Session) Export(ctx context.Context, network, member string) (*Exported, error) {
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
	text := ExportText(want, member)

	unlock, err := state.Lock(s.Dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	a := p.assigned[network]
	sn := s.State.Net(network)
	if !sn.Subnet.IsValid() {
		sn.Iface, sn.Subnet, sn.Port = n.IfaceName(), a.Subnet, a.Port
	}
	sm := sn.Member(member)
	sm.IP, sm.PubKey, sm.Applied = a.Addrs[member], pub, wgconf.Hash(text)
	if err := s.SaveState(); err != nil {
		return nil, err
	}
	return &Exported{Config: text, Missing: missing, Plan: p}, nil
}
