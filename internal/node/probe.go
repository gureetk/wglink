package node

import (
	"bufio"
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Facts is what Probe learns about a node.
type Facts struct {
	Hostname string
	UID      int
	OS       string // ID from /etc/os-release
	Have     map[string]bool
	Systemd  bool
	Module   bool // /sys/module/wireguard exists
	Forward4 bool
	Forward6 bool
	Addrs    []Addr
	Routes   []Route
	UDPPorts []uint16
	WGPorts  map[string]uint16 // listen port per WireGuard interface
	Managed  []string          // interfaces whose configs wglink wrote
	Ifaces   map[string]*Iface // the interfaces asked about
}

// Addr is an address on an interface.
type Addr struct {
	Iface  string
	Prefix netip.Prefix
}

// Route is a route from any table.
type Route struct {
	Prefix netip.Prefix
	Dev    string
	Table  string
}

// ConfState says whether a config file exists and who wrote it.
type ConfState int

const (
	ConfNone ConfState = iota
	ConfManaged
	ConfUnmanaged
)

// Iface is what Probe saw for one interface.
type Iface struct {
	Conf   ConfState
	Config []byte // public form: no private key, preshared keys masked
	PubKey string
	Up     bool
}

// Probe gathers facts, including details for the given interfaces.
func (n Node) Probe(ctx context.Context, ifaces []string) (*Facts, error) {
	if err := checkIfaces(ifaces); err != nil {
		return nil, err
	}
	out, err := n.run(ctx, "probe", map[string]string{"WG_IFACES": strings.Join(ifaces, " ")})
	if err != nil {
		return nil, err
	}
	f, err := parseProbe(out)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", n.Runner, err)
	}
	return f, nil
}

func parseProbe(out []byte) (*Facts, error) {
	f := &Facts{Have: map[string]bool{}, WGPorts: map[string]uint16{}, Ifaces: map[string]*Iface{}}
	var cur *Iface
	sawEnd := false
	for _, d := range parse(out) {
		switch d.key {
		case "hostname":
			f.Hostname = d.arg
		case "uid":
			f.UID, _ = strconv.Atoi(d.arg)
		case "os":
			f.OS = d.arg
		case "have":
			f.Have[d.arg] = true
		case "service":
			f.Systemd = d.arg == "systemd"
		case "module":
			f.Module = true
		case "forward4":
			f.Forward4 = d.arg == "1"
		case "forward6":
			f.Forward6 = d.arg == "1"
		case "managed":
			f.Managed = append(f.Managed, d.arg)
		case "iface":
			cur = &Iface{}
			f.Ifaces[d.arg] = cur
		case "conf":
			if cur != nil {
				cur.Conf = map[string]ConfState{"managed": ConfManaged, "unmanaged": ConfUnmanaged}[d.arg]
			}
		case "pub":
			if cur != nil {
				cur.PubKey = d.arg
			}
		case "up":
			if cur != nil {
				cur.Up = true
			}
		case "section":
			switch d.arg {
			case "addr":
				f.Addrs = parseAddrs(d.raw)
			case "route":
				f.Routes = parseRoutes(d.raw)
			case "udp":
				f.UDPPorts = parseUDP(d.raw)
			case "wgports":
				for _, l := range lines(d.raw) {
					fields := strings.Fields(l)
					if len(fields) == 2 {
						if p, err := strconv.ParseUint(fields[1], 10, 16); err == nil {
							f.WGPorts[fields[0]] = uint16(p)
						}
					}
				}
			case "config":
				if cur != nil {
					cur.Config = d.raw
				}
			}
		case "end":
			sawEnd = true
		}
	}
	if !sawEnd {
		return nil, fmt.Errorf("probe output ended early")
	}
	return f, nil
}

func lines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// parseAddrs reads "ip -o addr show" lines such as
// "2: eth0    inet 192.168.1.10/24 brd 192.168.1.255 scope global eth0".
func parseAddrs(raw []byte) []Addr {
	var out []Addr
	for _, l := range lines(raw) {
		f := strings.Fields(l)
		if len(f) < 4 || (f[2] != "inet" && f[2] != "inet6") {
			continue
		}
		iface, _, _ := strings.Cut(f[1], "@")
		if p, ok := parseAddr(f[3]); ok {
			out = append(out, Addr{Iface: iface, Prefix: p})
		}
	}
	return out
}

// Route types that describe local addresses, which parseAddrs already covers.
var skipRouteTypes = map[string]bool{"local": true, "broadcast": true, "anycast": true, "multicast": true, "nat": true}

// Route types that come before the destination.
var routeTypes = map[string]bool{"unicast": true, "unreachable": true, "prohibit": true, "blackhole": true, "throw": true}

// parseRoutes reads "ip -o route show table all" lines.
func parseRoutes(raw []byte) []Route {
	var out []Route
	for _, l := range lines(raw) {
		f := strings.Fields(l)
		if len(f) == 0 || skipRouteTypes[f[0]] {
			continue
		}
		if routeTypes[f[0]] {
			f = f[1:]
		}
		if len(f) == 0 || f[0] == "default" {
			continue
		}
		p, ok := parseAddr(f[0])
		if !ok {
			continue
		}
		r := Route{Prefix: p.Masked()}
		for i := 1; i+1 < len(f); i++ {
			switch f[i] {
			case "dev":
				r.Dev = f[i+1]
			case "table":
				r.Table = f[i+1]
			}
		}
		out = append(out, r)
	}
	return out
}

// parseUDP reads local ports from /proc/net/udp and udp6.
func parseUDP(raw []byte) []uint16 {
	seen := map[uint16]bool{}
	var out []uint16
	for _, l := range lines(raw) {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] == "sl" {
			continue
		}
		_, port, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(port, 16, 16)
		if err != nil || p == 0 || seen[uint16(p)] {
			continue
		}
		seen[uint16(p)] = true
		out = append(out, uint16(p))
	}
	return out
}
