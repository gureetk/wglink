package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Guest is a container or VM on a Proxmox VE host.
type Guest struct {
	Type   string // lxc or qemu
	VMID   int
	Name   string
	Status string         // running, stopped, ...
	Addrs  []netip.Prefix // IPv4 addresses, loopback and container bridges left out
	Err    string         // why the addresses are unknown, such as no guest agent
}

var pveNodeRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,62}$`)

// Discover lists the guests on this Proxmox VE host, with the addresses of
// the running ones. In a cluster, guests on other hosts are left out, since
// pct and qm only reach local ones. It returns the host's node name too.
func (n Node) Discover(ctx context.Context) (string, []Guest, error) {
	out, err := n.run(ctx, "discover", map[string]string{"WG_GUESTS": "", "WG_PVE_NODE": ""})
	if err != nil {
		return "", nil, err
	}
	var status, resources []byte
	sawEnd := false
	for _, d := range parse(out) {
		switch {
		case d.key == "section" && d.arg == "status":
			status = d.raw
		case d.key == "section" && d.arg == "guests":
			resources = d.raw
		case d.key == "end":
			sawEnd = true
		}
	}
	if !sawEnd {
		return "", nil, fmt.Errorf("%s: discover output ended early", n.Runner)
	}
	host, err := localNode(status)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", n.Runner, err)
	}
	var all []struct {
		Type     string  `json:"type"`
		VMID     int     `json:"vmid"`
		Name     string  `json:"name"`
		Node     string  `json:"node"`
		Status   string  `json:"status"`
		Template jsonInt `json:"template"`
	}
	if err := json.Unmarshal(resources, &all); err != nil {
		return "", nil, fmt.Errorf("%s: reading pvesh's guest list: %w", n.Runner, err)
	}
	var guests []Guest
	var running []string
	for _, g := range all {
		if g.Node != host || g.Template != 0 || (g.Type != "lxc" && g.Type != "qemu") {
			continue
		}
		guests = append(guests, Guest{Type: g.Type, VMID: g.VMID, Name: g.Name, Status: g.Status})
		if g.Status == "running" {
			running = append(running, g.Type+"/"+strconv.Itoa(g.VMID))
		}
	}
	sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
	if len(running) == 0 {
		return host, guests, nil
	}

	out, err = n.run(ctx, "discover", map[string]string{"WG_GUESTS": strings.Join(running, " "), "WG_PVE_NODE": host})
	if err != nil {
		return "", nil, err
	}
	byID := map[string]*Guest{}
	for i := range guests {
		byID[guests[i].Type+"/"+strconv.Itoa(guests[i].VMID)] = &guests[i]
	}
	for _, d := range parse(out) {
		switch d.key {
		case "guest":
			if g := byID[d.arg]; g != nil {
				g.Addrs, g.Err = guestAddrs(g.Type, d.raw)
			}
		case "failed":
			id, msg, _ := strings.Cut(d.arg, " ")
			if g := byID[id]; g != nil {
				g.Err = msg
			}
		}
	}
	return host, guests, nil
}

// localNode finds this host's name in pvesh's /cluster/status.
func localNode(status []byte) (string, error) {
	var entries []struct {
		Type  string  `json:"type"`
		Name  string  `json:"name"`
		Local jsonInt `json:"local"`
	}
	if err := json.Unmarshal(status, &entries); err != nil {
		return "", fmt.Errorf("reading pvesh's cluster status: %w", err)
	}
	for _, e := range entries {
		if e.Type == "node" && e.Local != 0 {
			if !pveNodeRE.MatchString(e.Name) {
				return "", fmt.Errorf("odd Proxmox node name %q", e.Name)
			}
			return e.Name, nil
		}
	}
	return "", fmt.Errorf("pvesh's cluster status doesn't name this host")
}

// guestAddrs reads a guest's interfaces as pvesh reports them.
func guestAddrs(kind string, raw []byte) ([]netip.Prefix, string) {
	type addr struct {
		name string
		p    netip.Prefix
	}
	var found []addr
	if kind == "lxc" {
		var ifaces []struct {
			Name string `json:"name"`
			Inet string `json:"inet"`
		}
		if err := json.Unmarshal(raw, &ifaces); err != nil {
			return nil, "unreadable interface list"
		}
		for _, i := range ifaces {
			for _, f := range strings.Fields(i.Inet) {
				if p, err := netip.ParsePrefix(f); err == nil {
					found = append(found, addr{i.Name, p})
				}
			}
		}
	} else {
		var res struct {
			Result []struct {
				Name  string `json:"name"`
				Addrs []struct {
					IP     string `json:"ip-address"`
					Prefix int    `json:"prefix"`
				} `json:"ip-addresses"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, "unreadable guest agent answer"
		}
		for _, i := range res.Result {
			for _, a := range i.Addrs {
				if ip, err := netip.ParseAddr(a.IP); err == nil {
					found = append(found, addr{i.Name, netip.PrefixFrom(ip, a.Prefix)})
				}
			}
		}
	}
	var out []netip.Prefix
	for _, a := range found {
		ip := a.p.Addr()
		if !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || bridgeLike(a.name) {
			continue
		}
		out = append(out, a.p)
	}
	return out, ""
}

// bridgeLike reports interfaces whose addresses peers can't dial from
// outside the guest, such as Docker's.
func bridgeLike(name string) bool {
	for _, p := range []string{"lo", "docker", "br-", "veth", "virbr", "cni", "flannel", "tailscale", "wg"} {
		if name == p || strings.HasPrefix(name, p) && p != "lo" {
			return true
		}
	}
	return false
}

// jsonInt reads the 0/1 flags pvesh prints, as numbers, booleans or strings.
type jsonInt int

func (j *jsonInt) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "1", "true":
		*j = 1
	default:
		*j = 0
	}
	return nil
}
