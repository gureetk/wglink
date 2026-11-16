// Package state records what wglink picked and applied, so later runs keep
// the same choices and can tell what changed. It holds no secrets.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"

	"github.com/gureetk/wglink/internal/atomicfile"
)

// FileName is the state file kept beside wglink.yaml.
const FileName = "wglink.state.json"

// State is the content of wglink.state.json.
type State struct {
	Version  int                 `json:"version"`
	Networks map[string]*Network `json:"networks"`
}

// Network records one network's assignments.
type Network struct {
	Subnet  netip.Prefix       `json:"subnet,omitzero"`
	Port    uint16             `json:"port,omitempty"`
	Members map[string]*Member `json:"members"`
}

// Member records one node's place in a network.
type Member struct {
	IP     netip.Addr `json:"ip,omitzero"`
	PubKey string     `json:"pubkey,omitempty"`
	// Applied is the hash of the config last written, with secrets removed.
	Applied string `json:"applied,omitempty"`
}

// New returns an empty state.
func New() *State {
	return &State{Version: 1, Networks: map[string]*Network{}}
}

// Load reads the state in dir; a missing file gives an empty state.
func Load(dir string) (*State, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	st := New()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d", path, st.Version)
	}
	if st.Networks == nil {
		st.Networks = map[string]*Network{}
	}
	for _, n := range st.Networks {
		if n.Members == nil {
			n.Members = map[string]*Member{}
		}
	}
	return st, nil
}

// Save writes the state to dir atomically.
func (st *State) Save(dir string) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, FileName), append(data, '\n'), 0o644)
}

// Net returns the named network, creating it if needed.
func (st *State) Net(name string) *Network {
	n := st.Networks[name]
	if n == nil {
		n = &Network{Members: map[string]*Member{}}
		st.Networks[name] = n
	}
	return n
}

// Member returns the named member, creating it if needed.
func (n *Network) Member(name string) *Member {
	m := n.Members[name]
	if m == nil {
		m = &Member{}
		n.Members[name] = m
	}
	return m
}

// Addrs returns the recorded member addresses.
func (n *Network) Addrs() map[string]netip.Addr {
	out := map[string]netip.Addr{}
	for name, m := range n.Members {
		if m.IP.IsValid() {
			out[name] = m.IP
		}
	}
	return out
}

// MemberNames returns member names in sorted order.
func (n *Network) MemberNames() []string {
	names := make([]string, 0, len(n.Members))
	for name := range n.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NodeName returns the member that owns pubkey in the network, if any.
func (n *Network) NodeName(pubkey string) (string, bool) {
	for name, m := range n.Members {
		if m.PubKey != "" && m.PubKey == pubkey {
			return name, true
		}
	}
	return "", false
}
