package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

// Logf reports progress.
type Logf func(format string, args ...any)

// Apply makes the nodes match the plan. It stops at the first failure;
// everything done before that is recorded, so running apply again resumes.
func (s *Session) Apply(ctx context.Context, p *Plan, logf Logf) error {
	if len(p.Problems) > 0 {
		return errors.New("the plan has problems; fix them and plan again")
	}
	unlock, err := state.Lock(s.Dir)
	if err != nil {
		return err
	}
	defer unlock()

	// Removals go first so their addresses and ports are free again.
	for _, r := range p.Removals {
		if !r.Gone {
			nd, err := s.Node(r.Node)
			if err != nil {
				return err
			}
			if err := s.timed(ctx, func(ctx context.Context) error { return nd.Remove(ctx, r.Iface) }); err != nil {
				return fmt.Errorf("removing %s from %s: %w", r.Iface, r.Node, err)
			}
			logf("%s: removed %s", r.Node, r.Iface)
		}
		s.forget(r.Network, r.Node)
		if err := s.SaveState(); err != nil {
			return err
		}
	}

	// Phase 1: every member gets a key, made on its own node.
	for _, np := range p.Networks {
		for _, mp := range np.Members {
			if !mp.NewKey || mp.Action == Skip {
				continue
			}
			nd, err := s.Node(mp.Node)
			if err != nil {
				return err
			}
			var pub string
			err = s.timed(ctx, func(ctx context.Context) (err error) {
				pub, _, err = nd.EnsureKey(ctx, np.Iface)
				return err
			})
			if err != nil {
				return fmt.Errorf("creating a key on %s: %w", mp.Node, err)
			}
			p.keys[np.Name][mp.Node] = pub
			s.State.Net(np.Name).Member(mp.Node).PubKey = pub
			if err := s.SaveState(); err != nil {
				return err
			}
			logf("%s: created a key for %s", mp.Node, np.Iface)
		}
	}

	// Phase 2: configs, one node at a time, now that every key is known.
	for _, np := range p.Networks {
		n := s.Inv.Networks[np.Name]
		sn := s.State.Net(np.Name)
		sn.Iface, sn.Subnet, sn.Port = np.Iface, np.Subnet, np.Port
		for _, mp := range np.Members {
			if mp.Action == Skip {
				continue
			}
			want := s.desired(ctx, p, n, mp.Node)
			text := wgconf.Render(want)
			sm := sn.Member(mp.Node)
			sm.IP, sm.PubKey = mp.Addr, p.keys[np.Name][mp.Node]
			if mp.Action == Keep {
				sm.Applied = wgconf.Hash(text)
				continue
			}
			if err := s.write(ctx, p, np, mp, want, text, logf); err != nil {
				return err
			}
			sm.Applied = wgconf.Hash(text)
			if err := s.SaveState(); err != nil {
				return err
			}
		}
		for _, name := range sn.MemberNames() {
			if _, ok := n.Member(name); !ok && !s.stillPresent(p, name, np.Iface) {
				delete(sn.Members, name)
			}
		}
	}
	s.pruneState(p)
	return s.SaveState()
}

// write installs one member's config and checks the node kept it intact.
func (s *Session) write(ctx context.Context, p *Plan, np *NetPlan, mp *MemberPlan, want wgconf.Interface, text []byte, logf Logf) error {
	nd, err := s.Node(mp.Node)
	if err != nil {
		return err
	}
	mode := wgconf.Restart
	if mp.Action == Live {
		// The plan used placeholders for new keys; check again with the real ones.
		if iface := p.facts[mp.Node].Ifaces[np.Iface]; iface != nil {
			if have, err := wgconf.Parse(iface.Config); err == nil {
				if change, _ := wgconf.Compare(have, want); change != wgconf.Restart {
					mode = wgconf.Live
				}
			}
		}
	}
	var got []byte
	err = s.timed(ctx, func(ctx context.Context) (err error) {
		got, err = nd.Write(ctx, node.Write{Iface: np.Iface, Config: text, Mode: mode, Adopt: p.opts.Adopt})
		return err
	})
	if err != nil {
		return fmt.Errorf("writing %s on %s: %w", np.Iface, mp.Node, err)
	}
	if wgconf.Hash(got) != wgconf.Hash(text) {
		return fmt.Errorf("%s on %s doesn't match what was sent; check the file by hand", np.Iface, mp.Node)
	}
	verb := map[Action]string{Create: "created", Live: "updated live", Restart: "restarted", Start: "started"}[mp.Action]
	if mode == wgconf.Restart && mp.Action == Live {
		verb = "restarted"
	}
	logf("%s: %s %s", mp.Node, verb, np.Iface)
	return nil
}

// stillPresent reports whether a former member still has the interface,
// in which case its state stays until a removal runs.
func (s *Session) stillPresent(p *Plan, nodeName, iface string) bool {
	if f := p.facts[nodeName]; f != nil {
		return f.Managed[iface] != ""
	}
	_, inInv := s.Inv.Nodes[nodeName]
	return inInv
}

// pruneState drops networks that are gone from wglink.yaml and have no members left.
func (s *Session) pruneState(p *Plan) {
	for name, sn := range s.State.Networks {
		if _, ok := s.Inv.Networks[name]; !ok && len(sn.Members) == 0 && p.inNetScope(name) {
			delete(s.State.Networks, name)
		}
	}
}

func (s *Session) forget(network, nodeName string) {
	if sn, ok := s.State.Networks[network]; ok {
		delete(sn.Members, nodeName)
	}
}

func (s *Session) timed(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	return fn(ctx)
}
