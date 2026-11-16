// Package ipam picks subnets, tunnel addresses and listen ports.
//
// Choices are sticky: anything recorded from an earlier run is kept as long
// as it is still valid, so adding or removing members renumbers nobody.
package ipam

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sort"

	"github.com/gureetk/wglink/internal/config"
)

// Request is everything Resolve needs.
type Request struct {
	Pool     netip.Prefix // automatic subnets are carved from here
	Prefix   int          // size of each automatic subnet
	BasePort uint16       // first automatic port tried
	Networks []Net
	// Used lists addresses and routes on each node, leaving out wglink's own interfaces.
	Used map[string][]Use
	// Ports lists UDP ports in use on each node, leaving out wglink's own interfaces.
	Ports map[string][]uint16
	// Seed makes random IPv6 subnets repeatable, so a plan and the apply
	// after it agree. Empty means truly random.
	Seed string
}

// Net is one network to resolve.
type Net struct {
	Name    string
	Subnet  netip.Prefix // fixed in wglink.yaml; zero means automatic
	IPv6    bool         // the network has IPv6 too
	Subnet6 netip.Prefix // fixed in wglink.yaml; zero means automatic
	Port    uint16       // fixed in wglink.yaml; zero means automatic
	Members []Member
	Prev    Assigned // what was recorded last time
}

// Member is a node and its requested address.
type Member struct {
	Node string
	Addr config.AddrSpec
}

// Use is a prefix already present on a node.
type Use struct {
	Prefix netip.Prefix
	What   string // e.g. "address on eth0"
}

// Assigned is the outcome for one network.
type Assigned struct {
	Subnet  netip.Prefix
	Subnet6 netip.Prefix // zero without IPv6
	Port    uint16
	Addrs   map[string]netip.Addr
	Addrs6  map[string]netip.Addr
}

// Resolve assigns every network a subnet, a port and member addresses.
func Resolve(req Request) (map[string]*Assigned, error) {
	r := &resolver{req: req, out: map[string]*Assigned{}}
	nets := append([]Net(nil), req.Networks...)
	sort.Slice(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })

	// Fixed and recorded subnets claim space first, then the rest are picked.
	for _, n := range nets {
		r.out[n.Name] = &Assigned{Addrs: map[string]netip.Addr{}, Addrs6: map[string]netip.Addr{}}
		switch {
		case n.Subnet.IsValid():
			r.claimSubnet(n, n.Subnet, "")
		case n.Prev.Subnet.IsValid():
			r.claimSubnet(n, n.Prev.Subnet, " (picked earlier; pin a different one with --subnet)")
		}
	}
	for _, n := range nets {
		if !r.out[n.Name].Subnet.IsValid() && !n.Subnet.IsValid() && !n.Prev.Subnet.IsValid() {
			r.pickSubnet(n)
		}
	}
	for _, n := range nets {
		switch {
		case n.Port != 0:
			r.claimPort(n, n.Port, "")
		case n.Prev.Port != 0:
			r.claimPort(n, n.Prev.Port, " (picked earlier; pin a different one with --port)")
		}
	}
	for _, n := range nets {
		if r.out[n.Name].Port == 0 && n.Port == 0 && n.Prev.Port == 0 {
			r.pickPort(n)
		}
	}
	for _, n := range nets {
		if r.out[n.Name].Subnet.IsValid() {
			r.assignAddrs(n)
		}
	}
	// IPv6: fixed and recorded subnets first, then random ones.
	for _, n := range nets {
		switch {
		case !n.IPv6:
		case n.Subnet6.IsValid():
			r.claimSubnet6(n, n.Subnet6, "")
		case n.Prev.Subnet6.IsValid():
			r.claimSubnet6(n, n.Prev.Subnet6, " (picked earlier; pin a different one with --ipv6)")
		}
	}
	for _, n := range nets {
		if n.IPv6 && !n.Subnet6.IsValid() && !n.Prev.Subnet6.IsValid() {
			r.pickSubnet6(n)
		}
	}
	for _, n := range nets {
		if r.out[n.Name].Subnet6.IsValid() {
			r.assignAddrs6(n)
		}
	}
	if len(r.errs) > 0 {
		return nil, errors.Join(r.errs...)
	}
	return r.out, nil
}

type resolver struct {
	req  Request
	out  map[string]*Assigned
	errs []error
}

func (r *resolver) fail(n Net, format string, args ...any) {
	r.errs = append(r.errs, fmt.Errorf("network %s: "+format, append([]any{n.Name}, args...)...))
}

// claimSubnet takes a fixed or recorded subnet after checking for clashes.
func (r *resolver) claimSubnet(n Net, p netip.Prefix, hint string) {
	ok := true
	for _, m := range n.Members {
		seen := map[netip.Prefix]bool{}
		for _, u := range r.req.Used[m.Node] {
			if u.Prefix.Overlaps(p) && !seen[u.Prefix] {
				seen[u.Prefix] = true
				r.fail(n, "subnet %s overlaps %s (%s) on %s%s", p, u.Prefix, u.What, m.Node, hint)
				ok = false
			}
		}
	}
	for name, a := range r.out {
		if a.Subnet.IsValid() && a.Subnet.Overlaps(p) && r.shareNode(name, n.Name) {
			r.fail(n, "subnet %s overlaps network %s (%s), which shares a node with it", p, name, a.Subnet)
			ok = false
		}
	}
	if ok {
		r.out[n.Name].Subnet = p
	}
}

// pickSubnet takes the first free subnet of the default size from the pool.
func (r *resolver) pickSubnet(n Net) {
	size := r.req.Prefix
	if size < r.req.Pool.Bits() {
		size = r.req.Pool.Bits()
	}
	step := uint64(1) << (32 - size)
	start := u32(r.req.Pool.Addr())
	end := uint64(start) + uint64(1)<<(32-r.req.Pool.Bits())
	for a := uint64(start); a < end; a += step {
		p := netip.PrefixFrom(addr4(uint32(a)), size)
		if r.subnetFree(n, p) {
			r.out[n.Name].Subnet = p
			return
		}
	}
	r.fail(n, "no free /%d left in pool %s; set defaults.pool or pin a subnet", size, r.req.Pool)
}

func (r *resolver) subnetFree(n Net, p netip.Prefix) bool {
	for _, a := range r.out {
		if a.Subnet.IsValid() && a.Subnet.Overlaps(p) {
			return false
		}
	}
	for _, m := range n.Members {
		for _, u := range r.req.Used[m.Node] {
			if u.Prefix.Overlaps(p) {
				return false
			}
		}
	}
	return true
}

func (r *resolver) claimPort(n Net, port uint16, hint string) {
	if node, ok := r.portInUse(n, port); ok {
		r.fail(n, "port %d is already in use on %s%s", port, node, hint)
		return
	}
	for name, a := range r.out {
		if name != n.Name && a.Port == port && r.shareNode(name, n.Name) {
			r.fail(n, "port %d is also used by network %s, which shares a node with it", port, name)
			return
		}
	}
	r.out[n.Name].Port = port
}

// pickPort takes the first port no other network uses and no member has open.
func (r *resolver) pickPort(n Net) {
	taken := map[uint16]bool{}
	for _, a := range r.out {
		taken[a.Port] = true
	}
	for p := uint32(r.req.BasePort); p <= 65535; p++ {
		port := uint16(p)
		if taken[port] {
			continue
		}
		if _, busy := r.portInUse(n, port); !busy {
			r.out[n.Name].Port = port
			return
		}
	}
	r.fail(n, "no free UDP port from %d up", r.req.BasePort)
}

func (r *resolver) portInUse(n Net, port uint16) (string, bool) {
	for _, m := range n.Members {
		for _, p := range r.req.Ports[m.Node] {
			if p == port {
				return m.Node, true
			}
		}
	}
	return "", false
}

// assignAddrs places pinned members, keeps recorded addresses, then fills the rest from .1 up.
func (r *resolver) assignAddrs(n Net) {
	a := r.out[n.Name]
	owner := map[netip.Addr]string{}
	var auto []string
	for _, m := range n.Members {
		if m.Addr.Kind == config.AddrAuto {
			auto = append(auto, m.Node)
			continue
		}
		ip, err := m.Addr.In(a.Subnet)
		if err != nil {
			r.fail(n, "member %s: %v", m.Node, err)
			continue
		}
		if other, dup := owner[ip]; dup {
			r.fail(n, "members %s and %s both want %s", other, m.Node, ip)
			continue
		}
		owner[ip] = m.Node
		a.Addrs[m.Node] = ip
	}
	var fresh []string
	for _, node := range auto {
		prev, ok := n.Prev.Addrs[node]
		if ok && usable(a.Subnet, prev) && owner[prev] == "" {
			owner[prev] = node
			a.Addrs[node] = prev
		} else {
			fresh = append(fresh, node)
		}
	}
	next := u32(a.Subnet.Addr()) + 1
	for _, node := range fresh {
		for ; ; next++ {
			ip := addr4(next)
			if !usable(a.Subnet, ip) {
				r.fail(n, "subnet %s is full; no address left for %s", a.Subnet, node)
				return
			}
			if owner[ip] == "" {
				owner[ip] = node
				a.Addrs[node] = ip
				break
			}
		}
	}
}

// shareNode reports whether two networks have a member in common.
func (r *resolver) shareNode(a, b string) bool {
	nodes := map[string]bool{}
	for _, n := range r.req.Networks {
		if n.Name == a {
			for _, m := range n.Members {
				nodes[m.Node] = true
			}
		}
	}
	for _, n := range r.req.Networks {
		if n.Name == b {
			for _, m := range n.Members {
				if nodes[m.Node] {
					return true
				}
			}
		}
	}
	return false
}

// usable reports whether ip is a host address inside p.
func usable(p netip.Prefix, ip netip.Addr) bool {
	if !p.Contains(ip) {
		return false
	}
	last := u32(p.Addr()) | (1<<(32-p.Bits()) - 1)
	return ip != p.Addr() && u32(ip) != last
}

func u32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func addr4(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// claimSubnet6 takes a fixed or recorded IPv6 subnet after checking for clashes.
func (r *resolver) claimSubnet6(n Net, p netip.Prefix, hint string) {
	if clash := r.clash6(n, p); clash != "" {
		r.fail(n, "IPv6 subnet %s overlaps %s%s", p, clash, hint)
		return
	}
	r.out[n.Name].Subnet6 = p
}

// pickSubnet6 picks a unique local /64 at random, as RFC 4193 asks, so
// networks set up apart can still be joined later.
func (r *resolver) pickSubnet6(n Net) {
	for i := range 10 {
		var b [16]byte
		b[0] = 0xfd
		if r.req.Seed != "" {
			sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d", r.req.Seed, n.Name, i))
			copy(b[1:6], sum[:])
		} else if _, err := rand.Read(b[1:6]); err != nil {
			r.fail(n, "picking an IPv6 subnet: %v", err)
			return
		}
		p := netip.PrefixFrom(netip.AddrFrom16(b), 64)
		if r.clash6(n, p) == "" {
			r.out[n.Name].Subnet6 = p
			return
		}
	}
	r.fail(n, "couldn't find a free IPv6 subnet; pin one with --ipv6=PREFIX")
}

// clash6 describes what an IPv6 subnet would overlap, or returns "".
func (r *resolver) clash6(n Net, p netip.Prefix) string {
	for _, m := range n.Members {
		for _, u := range r.req.Used[m.Node] {
			if u.Prefix.Overlaps(p) {
				return fmt.Sprintf("%s (%s) on %s", u.Prefix, u.What, m.Node)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r.out)) {
		if a := r.out[name]; a.Subnet6.IsValid() && a.Subnet6.Overlaps(p) && r.shareNode(name, n.Name) {
			return fmt.Sprintf("network %s (%s), which shares a node with it", name, a.Subnet6)
		}
	}
	return ""
}

// assignAddrs6 gives each member the IPv6 address with its IPv4 host number,
// so 10.99.0.5 goes with fd..::5.
func (r *resolver) assignAddrs6(n Net) {
	a := r.out[n.Name]
	for _, m := range n.Members {
		ip, ok := a.Addrs[m.Node]
		if !ok {
			continue
		}
		if a.Addrs6[m.Node], ok = Addr6(a.Subnet, a.Subnet6, ip); !ok {
			r.fail(n, "IPv6 subnet %s is too small for %s, host number %d", a.Subnet6, m.Node, u32(ip)-u32(a.Subnet.Addr()))
		}
	}
}

// Addr6 is the address in subnet6 with ip's host number in subnet.
func Addr6(subnet, subnet6 netip.Prefix, ip netip.Addr) (netip.Addr, bool) {
	host := u32(ip) - u32(subnet.Addr())
	if bits := 128 - subnet6.Bits(); bits < 32 && host>>bits != 0 {
		return netip.Addr{}, false
	}
	b := subnet6.Addr().As16()
	for i := range 4 {
		b[15-i] |= byte(host >> (8 * i))
	}
	return netip.AddrFrom16(b), true
}
