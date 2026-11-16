package config

import (
	"bytes"
	"fmt"
	"net/netip"

	"go.yaml.in/yaml/v4"

	"github.com/gureetk/wglink/internal/atomicfile"
)

// Doc is an editable wglink.yaml that keeps comments and key order.
type Doc struct {
	doc *yaml.Node
}

// Field is one key and value to write; a nil value deletes the key.
type Field struct {
	Key   string
	Value any
}

// Flow wraps a value that should be written inline, e.g. [a, b] or {via: h}.
type Flow struct{ V any }

// ParseDoc parses inventory data for editing.
func ParseDoc(data []byte) (*Doc, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Load(data, &doc); err != nil {
			return nil, loadError(FileName, err)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping()}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level must be a mapping", FileName)
	}
	return &Doc{doc: &doc}, nil
}

// Bytes encodes the document.
func (d *Doc) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc, err := yaml.NewDumper(&buf, yaml.WithIndent(2), yaml.WithLineWidth(-1))
	if err != nil {
		return nil, err
	}
	if err := enc.Dump(d.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return spaceSections(buf.Bytes()), nil
}

// spaceSections puts a blank line before each top-level key or comment block,
// since the encoder drops blank lines.
func spaceSections(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	out := make([][]byte, 0, len(lines)+8)
	for i, l := range lines {
		if i > 0 && len(l) > 0 && l[0] != ' ' && l[0] != '-' {
			prev := lines[i-1]
			if len(prev) > 0 && prev[0] != '#' {
				out = append(out, nil)
			}
		}
		out = append(out, l)
	}
	return bytes.Join(out, []byte("\n"))
}

// Inventory parses and validates the document as it now stands.
func (d *Doc) Inventory() (*Inventory, error) {
	data, err := d.Bytes()
	if err != nil {
		return nil, err
	}
	return Parse(FileName, data)
}

// SetNode adds the node, or replaces its fields if it exists.
func (d *Doc) SetNode(name string, fields ...Field) error {
	m, err := d.section("nodes")
	if err != nil {
		return err
	}
	n := mapping()
	if err := setFields(n, fields); err != nil {
		return err
	}
	setKey(m, name, n)
	return nil
}

// SetNodeFields changes some of an existing node's fields, keeping the rest.
func (d *Doc) SetNodeFields(name string, fields ...Field) error {
	m := lookup(d.root(), "nodes")
	var n *yaml.Node
	if m != nil {
		n = lookup(m, name)
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return fmt.Errorf("no node named %s", name)
	}
	return setFields(n, fields)
}

// RemoveNode deletes the node and reports whether it existed.
func (d *Doc) RemoveNode(name string) bool {
	m := lookup(d.root(), "nodes")
	return m != nil && deleteKey(m, name)
}

// AddNetwork creates a network; it fails if the name is taken.
func (d *Doc) AddNetwork(name string, fields ...Field) error {
	m, err := d.section("networks")
	if err != nil {
		return err
	}
	if lookup(m, name) != nil {
		return fmt.Errorf("network %s already exists", name)
	}
	n := mapping()
	if err := setFields(n, fields); err != nil {
		return err
	}
	if lookup(n, "members") == nil {
		setKey(n, "members", flowSeq())
	}
	setKey(m, name, n)
	return nil
}

// RemoveNetwork deletes the network and reports whether it existed.
func (d *Doc) RemoveNetwork(name string) bool {
	m := lookup(d.root(), "networks")
	return m != nil && deleteKey(m, name)
}

// SetNetworkFields sets or, for nil values, deletes keys on a network.
func (d *Doc) SetNetworkFields(name string, fields ...Field) error {
	n, err := d.network(name)
	if err != nil {
		return err
	}
	return setFields(n, fields)
}

// SetMember adds node to the network or changes its address spec.
func (d *Doc) SetMember(network, node string, addr AddrSpec) error {
	n, err := d.network(network)
	if err != nil {
		return err
	}
	members := lookup(n, "members")
	if members == nil || isNull(members) {
		members = flowSeq()
		setKey(n, "members", members)
	}
	if members.Kind == yaml.SequenceNode {
		if addr.Kind == AddrAuto {
			for _, c := range members.Content {
				if c.Value == node {
					return nil
				}
			}
			members.Content = append(members.Content, scalar(node))
			return nil
		}
		members = seqToMap(members)
		setKey(n, "members", members)
	}
	if members.Kind != yaml.MappingNode {
		return fmt.Errorf("network %s: members must be a list or a map", network)
	}
	if v := lookup(members, node); v != nil && v.Kind == yaml.MappingNode {
		setKey(v, "ip", addrNode(addr))
		return nil
	}
	setKey(members, node, addrNode(addr))
	return nil
}

// SetMemberRoutes sets the subnets a member routes for the others; none
// clears them. The member's entry grows into {ip, routes} or shrinks back.
func (d *Doc) SetMemberRoutes(network, node string, routes []netip.Prefix) error {
	n, err := d.network(network)
	if err != nil {
		return err
	}
	members := lookup(n, "members")
	if members == nil || members.Kind == yaml.SequenceNode {
		if len(routes) == 0 {
			return nil
		}
		members = seqToMap(members)
		setKey(n, "members", members)
	}
	if members.Kind != yaml.MappingNode {
		return fmt.Errorf("network %s: members must be a list or a map", network)
	}
	v := lookup(members, node)
	if v == nil {
		return fmt.Errorf("%s isn't in network %s", node, network)
	}
	if len(routes) == 0 {
		if v.Kind == yaml.MappingNode {
			deleteKey(v, "routes")
			ip := lookup(v, "ip")
			switch {
			case len(v.Content) == 0:
				setKey(members, node, scalar("auto"))
			case len(v.Content) == 2 && ip != nil:
				setKey(members, node, ip)
			}
		}
		if allAuto(members) {
			setKey(n, "members", mapToSeq(members))
		}
		return nil
	}
	if v.Kind != yaml.MappingNode {
		m := mapping()
		m.Style = yaml.FlowStyle
		if !isNull(v) && v.Value != "auto" {
			setKey(m, "ip", v)
		}
		setKey(members, node, m)
		v = m
	}
	list := flowSeq()
	for _, r := range routes {
		list.Content = append(list.Content, scalar(r.String()))
	}
	setKey(v, "routes", list)
	return nil
}

// addrNode quotes host parts, since a bare .10 would read as a number.
func addrNode(a AddrSpec) *yaml.Node {
	n := scalar(a.String())
	if a.Kind == AddrHost {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// RemoveMember takes node out of the network and reports whether it was there.
func (d *Doc) RemoveMember(network, node string) bool {
	n, err := d.network(network)
	if err != nil {
		return false
	}
	members := lookup(n, "members")
	if members == nil {
		return false
	}
	switch members.Kind {
	case yaml.SequenceNode:
		for i, c := range members.Content {
			if c.Value == node {
				members.Content = append(members.Content[:i], members.Content[i+1:]...)
				return true
			}
		}
	case yaml.MappingNode:
		if deleteKey(members, node) {
			if allAuto(members) {
				setKey(n, "members", mapToSeq(members))
			}
			return true
		}
	}
	return false
}

// Save validates the document and writes it to path atomically.
func (d *Doc) Save(path string) error {
	if _, err := d.Inventory(); err != nil {
		return err
	}
	data, err := d.Bytes()
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o644)
}

// Template returns the content of a new wglink.yaml.
func Template(pool netip.Prefix, prefix, port int) []byte {
	return fmt.Appendf(nil, `# wglink inventory: the machines wglink manages and the networks between them.
# Edit it by hand or with wglink commands, then run "wglink plan" to preview.
version: 1

defaults:
  pool: %s # automatic subnets come from here
  prefix: %d # size of each automatic subnet
  port: %d # first listen port tried

nodes: {}

networks: {}
`, pool, prefix, port)
}

func (d *Doc) root() *yaml.Node { return d.doc.Content[0] }

// section returns the top-level mapping for key, creating it if needed.
func (d *Doc) section(key string) (*yaml.Node, error) {
	v := lookup(d.root(), key)
	if v == nil || isNull(v) {
		v = mapping()
		setKey(d.root(), key, v)
	}
	if v.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", key)
	}
	v.Style = 0 // entries we add read best as blocks
	return v, nil
}

func (d *Doc) network(name string) (*yaml.Node, error) {
	m := lookup(d.root(), "networks")
	if m == nil {
		return nil, fmt.Errorf("no network named %s", name)
	}
	n := lookup(m, name)
	if n == nil {
		return nil, fmt.Errorf("no network named %s", name)
	}
	if n.Kind != yaml.MappingNode {
		n.Kind, n.Tag, n.Value, n.Content = yaml.MappingNode, "", "", nil
	}
	return n, nil
}

func setFields(m *yaml.Node, fields []Field) error {
	for _, f := range fields {
		if f.Value == nil {
			deleteKey(m, f.Key)
			continue
		}
		v, err := valueNode(f.Value)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Key, err)
		}
		setKey(m, f.Key, v)
	}
	return nil
}

func valueNode(v any) (*yaml.Node, error) {
	var n yaml.Node
	style := yaml.Style(0)
	if f, ok := v.(Flow); ok {
		v, style = f.V, yaml.FlowStyle
	}
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	n.Style |= style
	return &n, nil
}

func mapping() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }

func flowSeq() *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
}

func scalar(s string) *yaml.Node {
	var n yaml.Node
	n.SetString(s)
	return &n
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" }

func lookup(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setKey replaces key's value, keeping comments, or appends the key.
func setKey(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			old := m.Content[i+1]
			v.LineComment = old.LineComment
			if v.Kind == yaml.MappingNode && old.Kind == yaml.MappingNode {
				v.HeadComment, v.FootComment = old.HeadComment, old.FootComment
			}
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, scalar(key), v)
}

func deleteKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

func seqToMap(seq *yaml.Node) *yaml.Node {
	m := mapping()
	for _, c := range seq.Content {
		m.Content = append(m.Content, scalar(c.Value), scalar("auto"))
	}
	return m
}

func mapToSeq(m *yaml.Node) *yaml.Node {
	seq := flowSeq()
	for i := 0; i+1 < len(m.Content); i += 2 {
		seq.Content = append(seq.Content, scalar(m.Content[i].Value))
	}
	return seq
}

func allAuto(m *yaml.Node) bool {
	for i := 1; i < len(m.Content); i += 2 {
		v := m.Content[i]
		if !isNull(v) && !(v.Kind == yaml.ScalarNode && v.Value == "auto") {
			return false
		}
	}
	return true
}
