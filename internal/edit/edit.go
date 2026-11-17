// Package edit makes the changes to wglink.yaml that commands and the
// dashboard share, so both check them the same way.
package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/wgconf"
)

// Load reads the inventory in dir, both parsed and as an editable document.
func Load(dir string) (path string, doc *config.Doc, inv *config.Inventory, err error) {
	path = filepath.Join(dir, config.FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, nil, err
	}
	if inv, err = config.Parse(path, data); err != nil {
		return "", nil, nil, err
	}
	if doc, err = config.ParseDoc(data); err != nil {
		return "", nil, nil, err
	}
	return path, doc, inv, nil
}

// Node says how wglink reaches a new node, and how its peers do. Exactly one
// of SSH, Local, Exec, PCT, QM and Manual is set.
type Node struct {
	SSH       string // user@host, ssh://user@host:port or a ~/.ssh/config alias
	Local     bool
	Exec      []string
	PCT, QM   int    // a Proxmox guest, reached through the host Via
	Via       string //
	Manual    bool   // a device such as a phone, set up with wglink export
	PubKey    string // a manual device's own public key, if it has one
	Sudo      bool
	Endpoint  string
	Keepalive int
	Service   string
}

// AddNode adds a node to the inventory.
func AddNode(doc *config.Doc, inv *config.Inventory, name string, n Node) error {
	if !config.ValidNodeName(name) {
		return fmt.Errorf("%q: use lowercase letters, digits and hyphens, starting with a letter", name)
	}
	if _, ok := inv.Nodes[name]; ok {
		return fmt.Errorf("node %s already exists; remove it first or edit %s", name, config.FileName)
	}
	ways := 0
	for _, set := range []bool{n.SSH != "", n.Local, len(n.Exec) > 0, n.PCT != 0, n.QM != 0, n.Manual} {
		if set {
			ways++
		}
	}
	if ways != 1 {
		return errors.New("say how to reach it, one way: ssh, local, exec, pct, qm or manual")
	}
	var fields []config.Field
	switch {
	case n.SSH != "":
		fields = append(fields, config.Field{Key: "ssh", Value: n.SSH})
	case n.Local:
		fields = append(fields, config.Field{Key: "local", Value: true})
	case len(n.Exec) > 0:
		fields = append(fields, config.Field{Key: "exec", Value: config.Flow{V: n.Exec}})
	case n.PCT != 0 || n.QM != 0:
		kind, vmid := "pct", n.PCT
		if n.QM != 0 {
			kind, vmid = "qm", n.QM
		}
		if n.Via == "" {
			return fmt.Errorf("a %s guest needs the Proxmox host it's on (via)", kind)
		}
		fields = append(fields, config.Field{Key: kind, Value: config.Flow{V: map[string]any{"via": n.Via, "vmid": vmid}}})
	case n.Manual:
		m := map[string]any{}
		if n.PubKey != "" {
			if !wgconf.ValidKey(n.PubKey) {
				return fmt.Errorf("%q isn't a WireGuard public key", n.PubKey)
			}
			m["pubkey"] = n.PubKey
		}
		fields = append(fields, config.Field{Key: "manual", Value: config.Flow{V: m}})
		if n.Endpoint == "" {
			n.Endpoint = "none"
		}
	}
	if n.PubKey != "" && !n.Manual {
		return errors.New("a public key goes with a manual node")
	}
	if n.Sudo {
		fields = append(fields, config.Field{Key: "sudo", Value: true})
	}
	if n.Endpoint != "" {
		fields = append(fields, config.Field{Key: "endpoint", Value: n.Endpoint})
	}
	if n.Keepalive != 0 {
		fields = append(fields, config.Field{Key: "keepalive", Value: n.Keepalive})
	}
	if n.Service != "" {
		fields = append(fields, config.Field{Key: "service", Value: n.Service})
	}
	return doc.SetNode(name, fields...)
}

// Connection is nodes to join in a network.
type Connection struct {
	Network string                     // "" names a new network after the nodes
	Nodes   []string                   //
	Pins    map[string]config.AddrSpec // addresses to pin, by node
	Fields  []config.Field             // network settings to set
}

// Connect joins nodes in a network, creating it if needed, and returns its
// name. Members it already has keep their addresses and routes unless a pin
// says otherwise.
func Connect(doc *config.Doc, inv *config.Inventory, c Connection) (string, error) {
	for _, n := range c.Nodes {
		if _, ok := inv.Nodes[n]; !ok {
			return "", fmt.Errorf("no node named %s", n)
		}
	}
	name := c.Network
	if name == "" {
		name = AutoName(inv, c.Nodes)
	}
	net, exists := inv.Networks[name]
	switch {
	case !exists:
		if !config.ValidNetworkName(name) {
			return "", fmt.Errorf("%q: name a network with up to 12 lowercase letters, digits and hyphens", name)
		}
		if len(c.Nodes) < 2 {
			return "", errors.New("a new network needs at least two nodes")
		}
		if err := doc.AddNetwork(name, c.Fields...); err != nil {
			return "", err
		}
	case len(c.Fields) > 0:
		if err := doc.SetNetworkFields(name, c.Fields...); err != nil {
			return "", err
		}
	}
	for _, n := range c.Nodes {
		pin, pinned := c.Pins[n]
		if _, member := memberOf(net, n); member && !pinned {
			continue
		}
		if err := doc.SetMember(name, n, pin); err != nil {
			return "", err
		}
	}
	return name, nil
}

func memberOf(n *config.Network, node string) (config.Member, bool) {
	if n == nil {
		return config.Member{}, false
	}
	return n.Member(node)
}

// AutoName names a new network after two nodes, or net1, net2 and so on.
func AutoName(inv *config.Inventory, nodes []string) string {
	if len(nodes) == 2 {
		if n := strings.Join(nodes, "-"); config.ValidNetworkName(n) && inv.Networks[n] == nil {
			return n
		}
	}
	for k := 1; ; k++ {
		if n := "net" + strconv.Itoa(k); inv.Networks[n] == nil {
			return n
		}
	}
}
