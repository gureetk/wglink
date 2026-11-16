// Package config loads, validates and edits the wglink inventory file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v4"
)

// FileName is the inventory file name.
const FileName = "wglink.yaml"

// Defaults used when wglink.yaml leaves them out.
var (
	DefaultPool   = netip.MustParsePrefix("10.99.0.0/16")
	DefaultPrefix = 24
	DefaultPort   = 51820
)

// Inventory is the parsed wglink.yaml.
type Inventory struct {
	Version  int                 `yaml:"version"`
	Defaults Defaults            `yaml:"defaults"`
	Nodes    map[string]*Node    `yaml:"nodes"`
	Networks map[string]*Network `yaml:"networks"`
}

// Defaults control automatic choices.
type Defaults struct {
	Pool   netip.Prefix `yaml:"pool"`   // automatic subnets come from here
	Prefix int          `yaml:"prefix"` // size of each automatic subnet
	Port   int          `yaml:"port"`   // first listen port tried
}

// Node is a machine wglink manages.
type Node struct {
	Name string `yaml:"-"`

	// Reach methods; exactly one is set.
	SSH    string    `yaml:"ssh"`
	Local  bool      `yaml:"local"`
	Exec   []string  `yaml:"exec"`
	PCT    *GuestRef `yaml:"pct"`
	QM     *GuestRef `yaml:"qm"`
	Manual *Manual   `yaml:"manual"`

	Sudo      bool      `yaml:"sudo"`
	Endpoint  *Endpoint `yaml:"endpoint"`
	Keepalive int       `yaml:"keepalive"`
	Service   string    `yaml:"service"`
}

// Manual is a peer wglink can't reach, such as a phone. Other members add
// it as a peer, and "wglink export" prints its own config.
type Manual struct {
	PubKey string `yaml:"pubkey"` // empty until the first export
}

// GuestRef points at a Proxmox guest on a host node.
type GuestRef struct {
	Via  string `yaml:"via"`
	VMID int    `yaml:"vmid"`
}

// Reach names how wglink runs commands on a node.
type Reach string

const (
	ReachSSH    Reach = "ssh"
	ReachLocal  Reach = "local"
	ReachExec   Reach = "exec"
	ReachPCT    Reach = "pct"
	ReachQM     Reach = "qm"
	ReachManual Reach = "manual"
)

// Reach returns the node's reach method, or "" if none or several are set.
func (n *Node) Reach() Reach {
	var set []Reach
	if n.SSH != "" {
		set = append(set, ReachSSH)
	}
	if n.Local {
		set = append(set, ReachLocal)
	}
	if n.Exec != nil {
		set = append(set, ReachExec)
	}
	if n.PCT != nil {
		set = append(set, ReachPCT)
	}
	if n.QM != nil {
		set = append(set, ReachQM)
	}
	if n.Manual != nil {
		set = append(set, ReachManual)
	}
	if len(set) != 1 {
		return ""
	}
	return set[0]
}

// Network is a set of nodes joined by one WireGuard interface each.
type Network struct {
	Name string `yaml:"-"`

	Subnet    Subnet  `yaml:"subnet"`
	Port      int     `yaml:"port"`
	Iface     string  `yaml:"iface"`
	MTU       int     `yaml:"mtu"`
	Keepalive int     `yaml:"keepalive"`
	Members   Members `yaml:"members"`
}

// IfaceName returns the interface name used on every member.
func (n *Network) IfaceName() string {
	if n.Iface != "" {
		return n.Iface
	}
	return "wg-" + n.Name
}

// Member returns the named member, if present.
func (n *Network) Member(name string) (Member, bool) {
	for _, m := range n.Members {
		if m.Name == name {
			return m, true
		}
	}
	return Member{}, false
}

// Members lists a network's nodes, as written: a list of names or a map of name to address.
type Members []Member

// Member is one node in a network.
type Member struct {
	Name string
	Addr AddrSpec
}

// UnmarshalYAML accepts a sequence of names or a mapping of name to address spec.
func (m *Members) UnmarshalYAML(n *yaml.Node) error {
	*m = nil
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: members: want node names", c.Line)
			}
			*m = append(*m, Member{Name: c.Value})
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			spec, err := memberSpec(v)
			if err != nil {
				return fmt.Errorf("line %d: member %s: %w", v.Line, k.Value, err)
			}
			*m = append(*m, Member{Name: k.Value, Addr: spec})
		}
	case yaml.ScalarNode:
		if n.Tag != "!!null" {
			return fmt.Errorf("line %d: members: want a list of node names", n.Line)
		}
	default:
		return fmt.Errorf("line %d: members: want a list of node names", n.Line)
	}
	return nil
}

// memberSpec decodes a member value: an address spec or {ip: spec}.
func memberSpec(v *yaml.Node) (AddrSpec, error) {
	switch v.Kind {
	case yaml.ScalarNode:
		if v.Tag == "!!null" {
			return AddrSpec{}, nil
		}
		return ParseAddrSpec(v.Value)
	case yaml.MappingNode:
		var spec AddrSpec
		for i := 0; i+1 < len(v.Content); i += 2 {
			k, val := v.Content[i], v.Content[i+1]
			switch k.Value {
			case "ip":
				s, err := ParseAddrSpec(val.Value)
				if err != nil {
					return AddrSpec{}, err
				}
				spec = s
			default:
				return AddrSpec{}, unknownField(k.Value, "member")
			}
		}
		return spec, nil
	}
	return AddrSpec{}, errors.New("want an address like auto, .10 or 10.99.0.10")
}

// Load reads and validates the inventory at path.
func Load(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, data)
}

// Parse decodes and validates inventory data; name is used in errors.
func Parse(name string, data []byte) (*Inventory, error) {
	inv := &Inventory{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Load(data, inv, yaml.WithKnownFields()); err != nil {
			return nil, loadError(name, err)
		}
	}
	inv.fill()
	if err := inv.Validate(); err != nil {
		return nil, fmt.Errorf("%s:\n  %s", name, strings.ReplaceAll(err.Error(), "\n", "\n  "))
	}
	return inv, nil
}

// fill sets names from map keys and applies defaults.
func (inv *Inventory) fill() {
	if inv.Nodes == nil {
		inv.Nodes = map[string]*Node{}
	}
	if inv.Networks == nil {
		inv.Networks = map[string]*Network{}
	}
	for name, n := range inv.Nodes {
		if n == nil {
			n = &Node{}
			inv.Nodes[name] = n
		}
		n.Name = name
	}
	for name, n := range inv.Networks {
		if n == nil {
			n = &Network{}
			inv.Networks[name] = n
		}
		n.Name = name
	}
	if !inv.Defaults.Pool.IsValid() {
		inv.Defaults.Pool = DefaultPool
	}
	if inv.Defaults.Prefix == 0 {
		inv.Defaults.Prefix = DefaultPrefix
	}
	if inv.Defaults.Port == 0 {
		inv.Defaults.Port = DefaultPort
	}
}

// NodeNames returns node names in sorted order.
func (inv *Inventory) NodeNames() []string { return sortedKeys(inv.Nodes) }

// NetworkNames returns network names in sorted order.
func (inv *Inventory) NetworkNames() []string { return sortedKeys(inv.Networks) }

// NetworksOf returns the sorted names of networks the node belongs to.
func (inv *Inventory) NetworksOf(node string) []string {
	var out []string
	for _, name := range inv.NetworkNames() {
		if _, ok := inv.Networks[name].Member(node); ok {
			out = append(out, name)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ErrNotFound is returned, wrapped, when Find finds no wglink.yaml.
var ErrNotFound = errors.New("no " + FileName)

// Find returns the directory holding wglink.yaml, checking dir, then
// $WGLINK_DIR, the working directory and ~/.config/wglink.
func Find(dir string) (string, error) {
	if dir == "" {
		dir = os.Getenv("WGLINK_DIR")
	}
	if dir != "" {
		if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
			return "", fmt.Errorf("%w in %s; run \"wglink init\" there first", ErrNotFound, dir)
		}
		return dir, nil
	}
	candidates := []string{"."}
	if home, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "wglink"))
	}
	for _, d := range candidates {
		if _, err := os.Stat(filepath.Join(d, FileName)); err == nil {
			return d, nil
		}
	}
	return "", fmt.Errorf("%w here or in ~/.config/wglink; run \"wglink init\" first", ErrNotFound)
}

var (
	fieldNotFound = regexp.MustCompile(`field (\S+) not found in type config\.(\w+)`)
	linePrefix    = regexp.MustCompile(`^line (\d+): `)
)

// loadError rewrites YAML errors as "file:line: message".
func loadError(name string, err error) error {
	var list []*yaml.LoadError
	var le *yaml.LoadErrors
	var one *yaml.LoadError
	switch {
	case errors.As(err, &le):
		list = le.Errors
	case errors.As(err, &one):
		// A syntax error: the column helps find it.
		return fmt.Errorf("%s:%d:%d: %s", name, one.Mark.Line, one.Mark.Column, one.Message)
	default:
		return fmt.Errorf("%s: %w", name, err)
	}
	var msgs []string
	for _, e := range list {
		msg, line := e.Message, fmt.Sprint(e.Mark.Line)
		// Errors from our own unmarshalers carry a more precise line.
		if m := linePrefix.FindStringSubmatch(msg); m != nil {
			line, msg = m[1], msg[len(m[0]):]
		}
		if m := fieldNotFound.FindStringSubmatch(msg); m != nil {
			msg = unknownField(m[1], strings.ToLower(m[2])).Error()
		}
		msgs = append(msgs, fmt.Sprintf("%s:%s: %s", name, line, msg))
	}
	return errors.New(strings.Join(msgs, "\n"))
}

// fields lists the keys each section accepts, for typo suggestions.
var fields = map[string][]string{
	"inventory": {"version", "defaults", "nodes", "networks"},
	"defaults":  {"pool", "prefix", "port"},
	"node":      {"ssh", "local", "exec", "pct", "qm", "manual", "sudo", "endpoint", "keepalive", "service"},
	"guestref":  {"via", "vmid"},
	"manual":    {"pubkey"},
	"network":   {"subnet", "port", "iface", "mtu", "keepalive", "members"},
	"member":    {"ip"},
}

// unknownField reports a bad key, suggesting the closest valid one.
func unknownField(key, section string) error {
	best, bestDist := "", 3
	for _, f := range fields[section] {
		if d := editDistance(key, f); d < bestDist {
			best, bestDist = f, d
		}
	}
	if best != "" {
		return fmt.Errorf("unknown field %q in %s (did you mean %q?)", key, section, best)
	}
	return fmt.Errorf("unknown field %q in %s", key, section)
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
