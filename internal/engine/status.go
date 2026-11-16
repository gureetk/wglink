package engine

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/ipam"
	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

// WireGuard re-handshakes about every two minutes while packets flow, so a
// handshake this recent means the link carried traffic just now.
const freshHandshake = 3 * time.Minute

// Never is the handshake age of peers that haven't completed one.
const Never time.Duration = -1

// LinkState sums up traffic between two peers.
type LinkState string

const (
	LinkOK      LinkState = "ok"      // a recent handshake or a ping reply
	LinkIdle    LinkState = "idle"    // no recent handshake, and no ping to check
	LinkDown    LinkState = "down"    // nothing gets through
	LinkMissing LinkState = "missing" // an end isn't set up, or its interface is down
	LinkUnknown LinkState = "unknown" // neither end could be reached
)

// MemberState is how a member's interface looks.
type MemberState string

const (
	MemberUp          MemberState = "up"
	MemberDown        MemberState = "down"   // the interface is down or gone
	MemberNew         MemberState = "new"    // not applied yet
	MemberManual      MemberState = "manual" // wglink can't ask it; its peers tell
	MemberUnreachable MemberState = "unreachable"
)

// Health is what's known about traffic with one peer.
type Health struct {
	State     LinkState
	Handshake time.Duration // age of the latest handshake; Never if none
	RX, TX    uint64        // bytes received from and sent to the peer
	RTT       time.Duration // ping round trip; zero without a reply
	Note      string
}

// Report is the live state of an inventory's networks.
type Report struct {
	Networks []*NetReport
	Errors   map[string]error // nodes that couldn't be reached
}

// NetReport is one network's members and the link between each pair.
type NetReport struct {
	Name    string
	Iface   string
	Subnet  netip.Prefix // zero until applied
	Subnet6 netip.Prefix // zero without IPv6
	Port    uint16
	Members []*MemberReport
	Links   []*Link
}

// MemberReport is one member's interface.
type MemberReport struct {
	Node  string
	Addr  netip.Addr
	Addr6 netip.Addr // zero without IPv6
	State MemberState
	Drift bool // its config changed since the last apply
}

// Link is the tunnel between members A and B, with traffic as A counts it.
type Link struct {
	A, B string
	Health
}

// StatusOptions narrow Status.
type StatusOptions struct {
	Networks []string // all when empty
	NoPing   bool
}

// Status asks every member for its live WireGuard state, pings each peer
// through the tunnel and sums up every pair.
func (s *Session) Status(ctx context.Context, opts StatusOptions) (*Report, error) {
	names := opts.Networks
	if len(names) == 0 {
		names = s.Inv.NetworkNames()
	}
	var nets []*config.Network
	for _, name := range names {
		n, ok := s.Inv.Networks[name]
		if !ok {
			return nil, fmt.Errorf("no network named %s", name)
		}
		nets = append(nets, n)
	}

	ifaces := map[string][]string{}
	pings := map[string][]node.PingTarget{}
	for _, n := range nets {
		iface := s.liveIface(n)
		for _, m := range n.Members {
			if s.manual(m.Name) {
				continue
			}
			ifaces[m.Name] = append(ifaces[m.Name], iface)
			if opts.NoPing {
				continue
			}
			for _, peer := range n.Members {
				if r := s.recorded(n.Name, peer.Name); n.Linked(m.Name, peer.Name) && r != nil && r.IP.IsValid() {
					pings[m.Name] = append(pings[m.Name], node.PingTarget{Iface: iface, Addr: r.IP})
				}
			}
		}
	}
	var mu sync.Mutex
	got := map[string]*node.Status{}
	errs := s.each(ctx, keys(ifaces), func(ctx context.Context, name string) error {
		nd, err := s.Node(name)
		if err != nil {
			return err
		}
		st, err := nd.Status(ctx, ifaces[name], pings[name])
		if err != nil {
			return err
		}
		mu.Lock()
		got[name] = st
		mu.Unlock()
		return nil
	})

	r := &Report{Errors: errs}
	for _, n := range nets {
		r.Networks = append(r.Networks, s.netReport(n, got, errs))
	}
	return r, nil
}

// liveIface is the interface name last applied, which is what's running.
func (s *Session) liveIface(n *config.Network) string {
	if sn := s.State.Networks[n.Name]; sn != nil && sn.Iface != "" {
		return sn.Iface
	}
	return n.IfaceName()
}

// recorded returns what the last apply recorded for a member, or nil.
func (s *Session) recorded(network, member string) *state.Member {
	if sn := s.State.Networks[network]; sn != nil {
		return sn.Members[member]
	}
	return nil
}

func (s *Session) netReport(n *config.Network, got map[string]*node.Status, errs map[string]error) *NetReport {
	nr := &NetReport{Name: n.Name, Iface: s.liveIface(n)}
	if sn := s.State.Networks[n.Name]; sn != nil {
		nr.Subnet, nr.Subnet6, nr.Port = sn.Subnet, sn.Subnet6, sn.Port
	}
	views := map[string]*view{}
	for _, m := range n.Members {
		v := &view{name: m.Name, rec: s.recorded(n.Name, m.Name), err: errs[m.Name], manual: s.manual(m.Name)}
		if st := got[m.Name]; st != nil {
			v.now, v.iface, v.pings = st.Now, st.Ifaces[nr.Iface], st.Pings
		}
		views[m.Name] = v
		mr := v.member()
		if nr.Subnet6.IsValid() && mr.Addr.IsValid() {
			mr.Addr6, _ = ipam.Addr6(nr.Subnet, nr.Subnet6, mr.Addr)
		}
		nr.Members = append(nr.Members, mr)
	}
	for i, a := range n.Members {
		for _, b := range n.Members[i+1:] {
			if n.Linked(a.Name, b.Name) {
				nr.Links = append(nr.Links, link(nr.Iface, views[a.Name], views[b.Name]))
			}
		}
	}
	return nr
}

// view is one member's side of a network: what was applied there and what
// it reports now.
type view struct {
	name   string
	rec    *state.Member // nil until applied
	err    error         // set if it couldn't be reached
	manual bool          // wglink can't ask it
	now    time.Time     // its clock
	iface  *node.IfaceStatus
	pings  map[node.PingTarget]node.Ping
}

func (v *view) applied() bool { return v.rec != nil && v.rec.PubKey != "" }
func (v *view) up() bool      { return v.err == nil && v.iface != nil && v.iface.Up }

// asked reports whether the node itself reported its state.
func (v *view) asked() bool { return v.err == nil && !v.manual }

func (v *view) member() *MemberReport {
	m := &MemberReport{Node: v.name}
	if v.rec != nil {
		m.Addr = v.rec.IP
	}
	switch {
	case v.manual:
		m.State = MemberManual
		return m
	case v.err != nil:
		m.State = MemberUnreachable
	case !v.applied():
		m.State = MemberNew
	case v.up():
		m.State = MemberUp
	default:
		m.State = MemberDown
	}
	if v.err == nil && v.iface != nil && v.rec != nil && v.rec.Applied != "" {
		m.Drift = wgconf.Hash(v.iface.Config) != v.rec.Applied
	}
	return m
}

// peer finds other among v's peers by the key recorded for it.
func (v *view) peer(other *view) *node.PeerStatus {
	if v.iface == nil || !other.applied() {
		return nil
	}
	for i := range v.iface.Peers {
		if v.iface.Peers[i].PublicKey == other.rec.PubKey {
			return &v.iface.Peers[i]
		}
	}
	return nil
}

// link sums up the tunnel between a and b from whatever each end reports.
// One reachable end is enough to tell.
func link(iface string, a, b *view) *Link {
	l := &Link{A: a.name, B: b.name, Health: Health{Handshake: Never}}
	ends := []struct{ v, other *view }{{a, b}, {b, a}}
	for _, e := range ends {
		switch {
		case e.v.applied():
		case e.v.manual:
			l.State, l.Note = LinkMissing, e.v.name+" isn't exported yet; run wglink export"
			return l
		default:
			l.State, l.Note = LinkMissing, e.v.name+" isn't set up yet; run wglink apply"
			return l
		}
	}
	switch {
	case a.manual && b.manual:
		l.State, l.Note = LinkUnknown, "both ends are manual nodes"
		return l
	case !a.asked() && !b.asked():
		l.State, l.Note = LinkUnknown, "can't reach either end"
		return l
	}
	seen := map[*view]*node.PeerStatus{}
	for _, e := range ends {
		if !e.v.asked() {
			continue
		}
		if !e.v.up() {
			l.State, l.Note = LinkMissing, iface+" is down on "+e.v.name
			return l
		}
		p := e.v.peer(e.other)
		if p == nil {
			l.State, l.Note = LinkDown, fmt.Sprintf("%s doesn't list %s as a peer; run wglink apply", e.v.name, e.other.name)
			return l
		}
		seen[e.v] = p
		if age := handshakeAge(e.v.now, p.Handshake); age != Never && (l.Handshake == Never || age < l.Handshake) {
			l.Handshake = age
		}
	}
	if p := seen[a]; p != nil {
		l.RX, l.TX = p.RX, p.TX
	} else if p := seen[b]; p != nil {
		l.RX, l.TX = p.TX, p.RX
	}
	pinged, replied := false, false
	for _, e := range ends {
		if p, ok := e.v.pings[node.PingTarget{Iface: iface, Addr: e.other.rec.IP}]; ok {
			pinged = true
			if p.OK && (!replied || p.RTT < l.RTT) {
				replied, l.RTT = true, p.RTT
			}
		}
	}
	l.State, l.Note = judge(l.Handshake, pinged, replied, l.RTT)
	return l
}

// handshakeAge measures a handshake against the clock of the node that saw it.
func handshakeAge(now, hs time.Time) time.Duration {
	if hs.IsZero() {
		return Never
	}
	if now.IsZero() {
		now = time.Now()
	}
	return max(0, now.Sub(hs))
}

// judge rates a link from its latest handshake and any ping through it.
func judge(age time.Duration, pinged, replied bool, rtt time.Duration) (LinkState, string) {
	fresh := age != Never && age < freshHandshake
	hs := "no handshake yet"
	switch {
	case fresh:
		hs = "handshake " + ago(age)
	case age != Never:
		hs = "last handshake " + ago(age)
	}
	switch {
	case fresh && replied:
		return LinkOK, hs + ", ping " + roundRTT(rtt)
	case fresh && pinged:
		return LinkOK, hs + ", no ping reply"
	case fresh:
		return LinkOK, hs
	case replied:
		return LinkOK, "idle, ping " + roundRTT(rtt)
	case pinged:
		return LinkDown, hs + ", no ping reply"
	}
	return LinkIdle, hs
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func roundRTT(d time.Duration) string {
	if d < 10*time.Millisecond {
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

// IfaceReport is one WireGuard interface on a machine, for status without
// an inventory.
type IfaceReport struct {
	Name      string
	Network   string // set when wglink wrote the config
	Up        bool
	Addresses []netip.Prefix
	Port      uint16
	Peers     []*PeerReport
}

// PeerReport is one peer of an interface.
type PeerReport struct {
	Name      string // from wglink's comment in the config; empty if unnamed
	PublicKey string
	Endpoint  string
	Addr      netip.Addr // the tunnel address pinged
	Health
}

// LocalStatus reports every WireGuard interface on nd's machine, and the
// configs wglink wrote there, without needing an inventory.
func LocalStatus(ctx context.Context, nd node.Node, ping bool) ([]*IfaceReport, error) {
	st, err := nd.Status(ctx, node.AllInterfaces, nil)
	if err != nil {
		return nil, err
	}
	var out []*IfaceReport
	var targets []node.PingTarget
	for _, name := range keys(st.Ifaces) {
		is := st.Ifaces[name]
		r := &IfaceReport{Name: name, Up: is.Up, Port: is.ListenPort}
		named := map[string]string{}
		if c, err := wgconf.Parse(is.Config); err == nil {
			r.Network, r.Addresses = c.Network, c.Addresses
			if r.Port == 0 {
				r.Port = c.ListenPort
			}
			for _, p := range c.Peers {
				named[p.PublicKey] = p.Name
			}
		}
		for _, p := range is.Peers {
			pr := &PeerReport{Name: named[p.PublicKey], PublicKey: p.PublicKey, Endpoint: p.Endpoint, Addr: hostAddr(p.AllowedIPs)}
			pr.Handshake = handshakeAge(st.Now, p.Handshake)
			pr.RX, pr.TX = p.RX, p.TX
			if ping && pr.Addr.IsValid() {
				targets = append(targets, node.PingTarget{Iface: name, Addr: pr.Addr})
			}
			r.Peers = append(r.Peers, pr)
		}
		// Named peers first, by name; then the rest by key.
		slices.SortFunc(r.Peers, func(a, b *PeerReport) int {
			if (a.Name == "") != (b.Name == "") {
				return cmp.Compare(len(b.Name), len(a.Name))
			}
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.PublicKey, b.PublicKey))
		})
		out = append(out, r)
	}
	pings := map[node.PingTarget]node.Ping{}
	if len(targets) > 0 {
		ps, err := nd.Status(ctx, nil, targets)
		if err != nil {
			return nil, err
		}
		pings = ps.Pings
	}
	for _, r := range out {
		for _, p := range r.Peers {
			res, pinged := pings[node.PingTarget{Iface: r.Name, Addr: p.Addr}]
			if res.OK {
				p.RTT = res.RTT
			}
			p.State, p.Note = judge(p.Handshake, pinged, res.OK, res.RTT)
		}
	}
	return out, nil
}

// hostAddr picks the address to ping a peer at: its first single-address
// AllowedIP, preferring IPv4.
func hostAddr(allowed []netip.Prefix) netip.Addr {
	var v6 netip.Addr
	for _, p := range allowed {
		if !p.IsSingleIP() {
			continue
		}
		if p.Addr().Is4() {
			return p.Addr()
		}
		if !v6.IsValid() {
			v6 = p.Addr()
		}
	}
	return v6
}
