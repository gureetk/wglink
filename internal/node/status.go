package node

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Status is a node's live WireGuard state.
type Status struct {
	Ifaces map[string]*IfaceStatus
	Pings  map[netip.Addr]Ping
}

// IfaceStatus is one interface as wg show reports it.
type IfaceStatus struct {
	Up         bool
	PublicKey  string
	ListenPort uint16
	Peers      []PeerStatus
}

// PeerStatus is one peer line from wg show dump.
type PeerStatus struct {
	PublicKey    string
	PresharedKey bool
	Endpoint     string
	AllowedIPs   []netip.Prefix
	Handshake    time.Time // zero if never
	RX, TX       uint64
	Keepalive    int
}

// Ping is the result of one ping through the tunnel.
type Ping struct {
	OK  bool
	RTT time.Duration
}

// PingTarget is an address to ping out of an interface.
type PingTarget struct {
	Iface string
	Addr  netip.Addr
}

// Status reads the interfaces and pings the targets.
func (n Node) Status(ctx context.Context, ifaces []string, pings []PingTarget) (*Status, error) {
	if err := checkIfaces(ifaces); err != nil {
		return nil, err
	}
	var targets []string
	for _, p := range pings {
		if err := checkIfaces([]string{p.Iface}); err != nil || !p.Addr.IsValid() {
			return nil, fmt.Errorf("invalid ping target %v", p)
		}
		targets = append(targets, p.Iface+","+p.Addr.String())
	}
	out, err := n.run(ctx, "status", map[string]string{
		"WG_IFACES": strings.Join(ifaces, " "),
		"WG_PING":   strings.Join(targets, " "),
	})
	if err != nil {
		return nil, err
	}
	return parseStatus(out)
}

func parseStatus(out []byte) (*Status, error) {
	s := &Status{Ifaces: map[string]*IfaceStatus{}, Pings: map[netip.Addr]Ping{}}
	sawEnd := false
	for _, d := range parse(out) {
		switch d.key {
		case "iface":
			name, state, _ := strings.Cut(d.arg, " ")
			st := &IfaceStatus{Up: state == "up"}
			if st.Up {
				if err := st.parseDump(d.raw); err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
			}
			s.Ifaces[name] = st
		case "ping":
			f := strings.Fields(d.arg)
			if len(f) < 2 {
				continue
			}
			a, err := netip.ParseAddr(f[0])
			if err != nil {
				continue
			}
			p := Ping{OK: f[1] == "ok"}
			if p.OK && len(f) > 2 {
				if ms, err := strconv.ParseFloat(f[2], 64); err == nil {
					p.RTT = time.Duration(ms * float64(time.Millisecond))
				}
			}
			s.Pings[a] = p
		case "end":
			sawEnd = true
		}
	}
	if !sawEnd {
		return nil, fmt.Errorf("status output ended early")
	}
	return s, nil
}

// parseDump reads "wg show <iface> dump" output, with keys already masked.
func (st *IfaceStatus) parseDump(raw []byte) error {
	for i, l := range lines(raw) {
		f := strings.Split(l, "\t")
		if i == 0 {
			if len(f) < 3 {
				return fmt.Errorf("short interface line %q", l)
			}
			st.PublicKey = f[1]
			p, _ := strconv.ParseUint(f[2], 10, 16)
			st.ListenPort = uint16(p)
			continue
		}
		if len(f) < 8 {
			return fmt.Errorf("short peer line %q", l)
		}
		p := PeerStatus{PublicKey: f[0], PresharedKey: f[1] != "(none)"}
		if f[2] != "(none)" {
			p.Endpoint = f[2]
		}
		if f[3] != "(none)" {
			for _, a := range strings.Split(f[3], ",") {
				if pr, ok := parseAddr(a); ok {
					p.AllowedIPs = append(p.AllowedIPs, pr)
				}
			}
		}
		if hs, _ := strconv.ParseInt(f[4], 10, 64); hs > 0 {
			p.Handshake = time.Unix(hs, 0)
		}
		p.RX, _ = strconv.ParseUint(f[5], 10, 64)
		p.TX, _ = strconv.ParseUint(f[6], 10, 64)
		if f[7] != "off" {
			p.Keepalive, _ = strconv.Atoi(f[7])
		}
		st.Peers = append(st.Peers, p)
	}
	return nil
}
