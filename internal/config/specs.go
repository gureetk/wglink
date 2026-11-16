package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Subnet is a network's IPv4 subnet; the zero value means automatic.
type Subnet struct {
	netip.Prefix
}

// Auto reports whether wglink picks the subnet.
func (s Subnet) Auto() bool { return !s.IsValid() }

// ParseSubnet parses "auto" or an IPv4 CIDR such as 10.99.0.0/24.
func ParseSubnet(s string) (Subnet, error) {
	if s == "" || s == "auto" {
		return Subnet{}, nil
	}
	if p, err := netip.ParsePrefix(s); err == nil && !p.Addr().Is4() {
		return Subnet{}, fmt.Errorf("subnet %s: must be IPv4", s)
	}
	p, err := parseNetPrefix(s, 8, 30)
	if err != nil {
		return Subnet{}, err
	}
	return Subnet{p}, nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Subnet) UnmarshalText(b []byte) error {
	v, err := ParseSubnet(string(b))
	*s = v
	return err
}

// MarshalText implements encoding.TextMarshaler.
func (s Subnet) MarshalText() ([]byte, error) {
	if s.Auto() {
		return []byte("auto"), nil
	}
	return []byte(s.Prefix.String()), nil
}

// parseNetPrefix parses a CIDR that must be a network address with bits in [lo, hi].
func parseNetPrefix(s string, lo, hi int) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a CIDR like 10.99.0.0/24", s)
	}
	if p != p.Masked() {
		return netip.Prefix{}, fmt.Errorf("%s has host bits set; did you mean %s?", s, p.Masked())
	}
	if p.Bits() < lo || p.Bits() > hi {
		return netip.Prefix{}, fmt.Errorf("%s: prefix length must be between /%d and /%d", s, lo, hi)
	}
	return p, nil
}

// AddrKind says how a member's tunnel address is chosen.
type AddrKind int

const (
	AddrAuto  AddrKind = iota // lowest free address
	AddrHost                  // fixed host part, e.g. ".10"
	AddrFixed                 // full address
)

// AddrSpec is a member's requested tunnel address.
type AddrSpec struct {
	Kind AddrKind
	Host []byte     // last octets for AddrHost
	Addr netip.Addr // for AddrFixed
}

// ParseAddrSpec parses "auto", ".N", ".N.N" or a full IPv4 address.
func ParseAddrSpec(s string) (AddrSpec, error) {
	switch {
	case s == "" || s == "auto":
		return AddrSpec{}, nil
	case strings.HasPrefix(s, "."):
		parts := strings.Split(s[1:], ".")
		if len(parts) > 2 {
			return AddrSpec{}, fmt.Errorf("address %q: use .N or .N.N", s)
		}
		host := make([]byte, len(parts))
		for i, p := range parts {
			n, err := strconv.ParseUint(p, 10, 8)
			if err != nil || p == "" || (len(p) > 1 && p[0] == '0') {
				return AddrSpec{}, fmt.Errorf("address %q: %q is not a number from 0 to 255", s, p)
			}
			host[i] = byte(n)
		}
		return AddrSpec{Kind: AddrHost, Host: host}, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return AddrSpec{}, fmt.Errorf("address %q: want auto, .N, .N.N or an IPv4 address", s)
	}
	return AddrSpec{Kind: AddrFixed, Addr: a}, nil
}

// String returns the spec as written in wglink.yaml.
func (a AddrSpec) String() string {
	switch a.Kind {
	case AddrHost:
		parts := make([]string, len(a.Host))
		for i, b := range a.Host {
			parts[i] = strconv.Itoa(int(b))
		}
		return "." + strings.Join(parts, ".")
	case AddrFixed:
		return a.Addr.String()
	}
	return "auto"
}

// In returns the pinned address inside subnet. It fails for AddrAuto.
func (a AddrSpec) In(subnet netip.Prefix) (netip.Addr, error) {
	var addr netip.Addr
	switch a.Kind {
	case AddrFixed:
		addr = a.Addr
	case AddrHost:
		b := subnet.Addr().As4()
		copy(b[4-len(a.Host):], a.Host)
		addr = netip.AddrFrom4(b)
	default:
		return netip.Addr{}, fmt.Errorf("address is automatic")
	}
	if !subnet.Contains(addr) {
		return netip.Addr{}, fmt.Errorf("%s (from %s) is outside subnet %s", addr, a, subnet)
	}
	if addr == subnet.Addr() || addr == lastAddr(subnet) {
		return netip.Addr{}, fmt.Errorf("%s is the network or broadcast address of %s", addr, subnet)
	}
	return addr, nil
}

// lastAddr returns the highest address in an IPv4 prefix.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v |= 1<<(32-p.Bits()) - 1
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// Endpoint is the address peers dial to reach a node.
type Endpoint struct {
	Host string // DNS name or IP literal; empty with None
	Port uint16 // 0 means the network's port
	None bool   // peers never dial this node
}

// ParseEndpoint parses "none", "host", "host:port", "[v6]" or "[v6]:port".
func ParseEndpoint(s string) (Endpoint, error) {
	if s == "none" {
		return Endpoint{None: true}, nil
	}
	host, port, hasPort := s, "", false
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.Index(s, "]")
		if end < 0 {
			return Endpoint{}, fmt.Errorf("endpoint %q: missing ]", s)
		}
		host = s[1:end]
		if a, err := netip.ParseAddr(host); err != nil || !a.Is6() {
			return Endpoint{}, fmt.Errorf("endpoint %q: brackets need an IPv6 address", s)
		}
		if rest := s[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return Endpoint{}, fmt.Errorf("endpoint %q: unexpected %q after ]", s, rest)
			}
			port, hasPort = rest[1:], true
		}
	case strings.Count(s, ":") == 1:
		host, port, _ = strings.Cut(s, ":")
		hasPort = true
	}
	if !validHost(host) {
		return Endpoint{}, fmt.Errorf("endpoint %q: %q is not a hostname or IP address", s, host)
	}
	e := Endpoint{Host: host}
	if hasPort {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return Endpoint{}, fmt.Errorf("endpoint %q: port must be 1-65535", s)
		}
		e.Port = uint16(n)
	}
	return e, nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (e *Endpoint) UnmarshalText(b []byte) error {
	v, err := ParseEndpoint(string(b))
	*e = v
	return err
}

// MarshalText implements encoding.TextMarshaler.
func (e Endpoint) MarshalText() ([]byte, error) { return []byte(e.String()), nil }

// String returns the endpoint as written in wglink.yaml.
func (e Endpoint) String() string {
	if e.None {
		return "none"
	}
	if e.Port == 0 {
		if strings.Contains(e.Host, ":") {
			return "[" + e.Host + "]"
		}
		return e.Host
	}
	return e.Dial(e.Port)
}

// Dial returns host:port for a peer config, using port unless the endpoint has its own.
func (e Endpoint) Dial(port uint16) string {
	if e.Port != 0 {
		port = e.Port
	}
	h := e.Host
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(int(port))
}

// validHost reports whether s is an IP literal or a DNS hostname.
func validHost(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return !strings.Contains(s, "%")
	}
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}
