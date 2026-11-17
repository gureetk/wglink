package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/edit"
)

type (
	// formMsg opens a form once the inventory is read.
	formMsg struct {
		screen screen
		inv    *config.Inventory
		err    error
	}
	addedMsg struct {
		name string
		err  error
	}
	connectedMsg struct {
		name string
		err  error
	}
)

// openForm reads the inventory, then opens the form for screen.
func (m Model) openForm(s screen) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		inv, err := be.Inventory()
		return formMsg{s, inv, err}
	}
}

// nodeForm asks for a new node: its name and how wglink reaches it.
func nodeForm(inv *config.Inventory) *form {
	var hosts []string
	for _, name := range inv.NodeNames() {
		switch inv.Nodes[name].Reach() {
		case config.ReachSSH, config.ReachLocal, config.ReachExec:
			hosts = append(hosts, name)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{"none yet"}
	}
	when := func(kinds ...string) func(*form) bool {
		return func(f *form) bool { return slices.Contains(kinds, f.chosen("reach")) }
	}
	reach := choice("reach", "Reach", "ssh", "local", "pct", "qm", "manual")
	reach.hint = func(f *form) string {
		return map[string]string{
			"ssh":    "over SSH",
			"local":  "the machine wglink runs on",
			"pct":    "a Proxmox container, through its host",
			"qm":     "a Proxmox VM, through its host's guest agent",
			"manual": "a phone or other device; set up with wglink export",
		}[f.chosen("reach")]
	}
	ssh := textInput("ssh", "SSH", "root@192.168.1.11")
	vmid := textInput("vmid", "VM ID", "101")
	via := choice("via", "Host", hosts...)
	pubkey := textInput("pubkey", "Public key", "blank: made by wglink export")
	endpoint := textInput("endpoint", "Endpoint", "")
	endpoint.hint = func(f *form) string {
		if f.text("endpoint") != "" {
			return ""
		}
		return map[string]string{
			"ssh":    "blank: the SSH host",
			"local":  "the address peers dial",
			"pct":    "the guest's address",
			"qm":     "the guest's address",
			"manual": "blank: it dials out",
		}[f.chosen("reach")]
	}
	sudo := toggle("sudo", "Sudo", "become root with sudo -n")
	ssh.show, sudo.show = when("ssh"), when("ssh", "local")
	vmid.show, via.show = when("pct", "qm"), when("pct", "qm")
	pubkey.show = when("manual")
	return &form{title: "Add a node", submit: "add", fields: []*field{
		textInput("name", "Name", "pve2"), reach, ssh, vmid, via, pubkey, endpoint, sudo,
	}}
}

func (m Model) submitNode() (tea.Model, tea.Cmd) {
	f := m.form
	name, kind := f.text("name"), f.chosen("reach")
	n := edit.Node{Endpoint: f.text("endpoint"), Sudo: f.get("sudo").on && (kind == "ssh" || kind == "local")}
	switch kind {
	case "ssh":
		if n.SSH = f.text("ssh"); n.SSH == "" {
			f.err = "Say where to SSH to, such as root@192.168.1.11."
			return m, nil
		}
	case "local":
		n.Local = true
	case "pct", "qm":
		id, err := strconv.Atoi(f.text("vmid"))
		if err != nil || id <= 0 {
			f.err = "The VM ID is a number, such as 101."
			return m, nil
		}
		if n.Via = f.chosen("via"); n.Via == "none yet" {
			f.err = "Add the Proxmox host as a node first."
			return m, nil
		}
		if kind == "pct" {
			n.PCT = id
		} else {
			n.QM = id
		}
	case "manual":
		n.Manual, n.PubKey = true, f.text("pubkey")
	}
	be := m.be
	return m, func() tea.Msg { return addedMsg{name, be.AddNode(name, n)} }
}

// connectForm asks which nodes to join, in a new network or one there is.
func connectForm(inv *config.Inventory) *form {
	fresh := func(f *form) bool { return inv.Networks[f.text("network")] == nil }
	network := textInput("network", "Network", "blank: named after the nodes")
	network.hint = func(f *form) string {
		if n := f.text("network"); n != "" && !fresh(f) {
			return "joining " + n + "; its settings stay"
		}
		return ""
	}
	f := &form{title: "Connect nodes", submit: "connect and plan", fields: []*field{network}}
	names := inv.NodeNames()
	for i, name := range names {
		label := ""
		if i == 0 {
			label = "Nodes"
		}
		f.fields = append(f.fields, toggle("node:"+name, label, name))
	}
	hub := choice("hub", "Hub", append([]string{"none: a full mesh"}, names...)...)
	ipv6 := toggle("ipv6", "IPv6", "a unique local /64 inside the tunnels")
	psk := toggle("psk", "Preshared", "a key for each pair of peers")
	hub.show, ipv6.show, psk.show = fresh, fresh, fresh
	f.fields = append(f.fields, hub, ipv6, psk)
	return f
}

func (m Model) submitConnect() (tea.Model, tea.Cmd) {
	f := m.form
	c := edit.Connection{Network: f.text("network")}
	for _, x := range f.fields {
		if name, ok := strings.CutPrefix(x.key, "node:"); ok && x.on {
			c.Nodes = append(c.Nodes, name)
		}
	}
	if len(c.Nodes) == 0 {
		f.err = "Check the nodes to connect with space."
		return m, nil
	}
	if f.get("hub").show(f) {
		if hub := f.chosen("hub"); f.get("hub").choice > 0 {
			if !slices.Contains(c.Nodes, hub) {
				f.err = "The hub has to be one of the checked nodes."
				return m, nil
			}
			c.Fields = append(c.Fields, config.Field{Key: "hub", Value: hub})
		}
		if f.get("ipv6").on {
			c.Fields = append(c.Fields, config.Field{Key: "ipv6", Value: "auto"})
		}
		if f.get("psk").on {
			c.Fields = append(c.Fields, config.Field{Key: "psk", Value: true})
		}
	}
	be := m.be
	return m, func() tea.Msg {
		name, err := be.Connect(c)
		return connectedMsg{name, err}
	}
}

// formKey handles keys while a form is open.
func (m Model) formKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.screen, m.form = dashboard, nil
		return m, nil
	case "enter":
		m.form.err = ""
		if m.screen == addingNode {
			return m.submitNode()
		}
		return m.submitConnect()
	case "tab", "down":
		return m, m.form.move(1)
	case "shift+tab", "up":
		return m, m.form.move(-1)
	}
	return m, m.form.key(msg)
}
