// Package wgconf renders and parses wg-quick configs.
//
// Rendered configs never contain secrets: the node inserts its private key
// after [Interface], and replaces "PresharedKey = @peer" with the shared key.
package wgconf

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MarkerPrefix starts the first line of every config wglink writes.
const MarkerPrefix = "# Managed by wglink"

// Interface is one wg-quick interface.
type Interface struct {
	Network    string // from the marker line
	Addresses  []netip.Prefix
	ListenPort uint16
	MTU        int
	Forward    bool // pass traffic between peers, as a hub does
	Peers      []Peer
}

// ForwardHook turns on IPv4 forwarding for the interface alone when it comes
// up, so a hub can pass traffic between its peers.
const ForwardHook = "echo 1 > /proc/sys/net/ipv4/conf/%i/forwarding"

// Peer is one [Peer] section.
type Peer struct {
	Name         string // from the comment line after [Peer]
	PublicKey    string
	PresharedKey bool // rendered as a placeholder the node fills in
	AllowedIPs   []netip.Prefix
	Endpoint     string // host:port; empty when the peer can't be dialed
	Keepalive    int
}

// Marker returns the first line of a config for network.
func Marker(network string) string {
	return fmt.Sprintf("%s (network %s). Edits here are replaced on the next apply.", MarkerPrefix, network)
}

// Render returns the config text with no private or preshared keys.
func Render(i Interface) []byte {
	var b bytes.Buffer
	fmt.Fprintln(&b, Marker(i.Network))
	fmt.Fprintln(&b, "[Interface]")
	fmt.Fprintf(&b, "Address = %s\n", joinPrefixes(i.Addresses))
	if i.ListenPort != 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", i.ListenPort)
	}
	if i.MTU != 0 {
		fmt.Fprintf(&b, "MTU = %d\n", i.MTU)
	}
	if i.Forward {
		fmt.Fprintf(&b, "PostUp = %s\n", ForwardHook)
	}
	peers := slices.Clone(i.Peers)
	slices.SortFunc(peers, func(a, b Peer) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range peers {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "[Peer]")
		fmt.Fprintf(&b, "# %s\n", p.Name)
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		if p.PresharedKey {
			fmt.Fprintf(&b, "PresharedKey = @%s\n", p.Name)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", joinPrefixes(p.AllowedIPs))
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.Keepalive != 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
		}
	}
	return b.Bytes()
}

func joinPrefixes(ps []netip.Prefix) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}

var (
	privateKeyLine   = regexp.MustCompile(`(?im)^[ \t]*PrivateKey[ \t]*=.*\n?`)
	presharedKeyLine = regexp.MustCompile(`(?im)^([ \t]*PresharedKey[ \t]*=).*$`)
)

// Public removes the private key and masks preshared keys. Nodes apply the
// same transform before sending a config back, so hashes compare equal.
func Public(text []byte) []byte {
	text = privateKeyLine.ReplaceAll(text, nil)
	return presharedKeyLine.ReplaceAll(text, []byte("$1 (set)"))
}

// Hash identifies a config by its public form.
func Hash(text []byte) string {
	sum := sha256.Sum256(Public(text))
	return hex.EncodeToString(sum[:])
}

// IsManaged reports whether text starts with wglink's marker.
func IsManaged(text []byte) bool { return bytes.HasPrefix(text, []byte(MarkerPrefix)) }

var markerNetwork = regexp.MustCompile(`^# Managed by wglink \(network ([a-z0-9-]+)\)`)

// Parse reads a wg-quick config. Keys are case-insensitive, as in wg-quick.
func Parse(text []byte) (Interface, error) {
	var i Interface
	var peer *Peer
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			if m := markerNetwork.FindStringSubmatch(line); m != nil {
				i.Network = m[1]
			}
		}
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			if peer != nil && peer.Name == "" && peer.PublicKey == "" {
				peer.Name = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			}
			continue
		case strings.EqualFold(line, "[Interface]"):
			section, peer = "interface", nil
			continue
		case strings.EqualFold(line, "[Peer]"):
			i.Peers = append(i.Peers, Peer{})
			peer, section = &i.Peers[len(i.Peers)-1], "peer"
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return i, fmt.Errorf("line %d: want key = value", n)
		}
		key, val = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)
		// wg-quick drops everything after #; keys are base64 and never contain it.
		if j := strings.Index(val, "#"); j >= 0 {
			val = strings.TrimSpace(val[:j])
		}
		var err error
		switch section {
		case "interface":
			err = i.set(key, val)
		case "peer":
			err = peer.set(key, val)
		default:
			err = fmt.Errorf("%s outside a section", key)
		}
		if err != nil {
			return i, fmt.Errorf("line %d: %w", n, err)
		}
	}
	return i, sc.Err()
}

func (i *Interface) set(key, val string) error {
	switch key {
	case "address":
		ps, err := parsePrefixes(val, false)
		i.Addresses = append(i.Addresses, ps...)
		return err
	case "listenport":
		n, err := strconv.ParseUint(val, 10, 16)
		i.ListenPort = uint16(n)
		return err
	case "mtu":
		n, err := strconv.Atoi(val)
		i.MTU = n
		return err
	case "postup":
		if val == ForwardHook {
			i.Forward = true
		}
	}
	return nil // other wg-quick keys don't affect wglink
}

func (p *Peer) set(key, val string) error {
	switch key {
	case "publickey":
		p.PublicKey = val
	case "presharedkey":
		p.PresharedKey = val != ""
	case "allowedips":
		ps, err := parsePrefixes(val, true)
		p.AllowedIPs = append(p.AllowedIPs, ps...)
		return err
	case "endpoint":
		p.Endpoint = val
	case "persistentkeepalive":
		if val == "off" {
			return nil
		}
		n, err := strconv.Atoi(val)
		p.Keepalive = n
		return err
	}
	return nil
}

// parsePrefixes parses "a/b, c/d"; bare addresses become host prefixes.
func parsePrefixes(s string, mask bool) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return out, err
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return out, err
		}
		if mask {
			p = p.Masked()
		}
		out = append(out, p)
	}
	return out, nil
}
