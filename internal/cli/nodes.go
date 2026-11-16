package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/engine"
	"github.com/gureetk/wglink/internal/wgconf"
)

func (a *app) newNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "node",
		Short:   "Add, list and remove machines",
		GroupID: "inventory",
	}
	cmd.AddCommand(a.newNodeAddCmd(), a.newNodeListCmd(), a.newNodeRmCmd())
	return cmd
}

func (a *app) newNodeAddCmd() *cobra.Command {
	var f struct {
		ssh, exec, via, endpoint, service, pubkey string
		local, manual, sudo, noCheck              bool
		pct, qm, keepalive                        int
	}
	cmd := &cobra.Command{
		Use:   "add NAME (--ssh DEST | --local | --exec CMD | --pct VMID --via HOST | --qm VMID --via HOST | --manual)",
		Short: "Add a machine, or a device such as a phone",
		Example: `  wglink node add pve1 --ssh root@192.168.1.10
  wglink node add nas --ssh admin@nas.lan --sudo
  wglink node add pi --ssh pi-garage                 # a Host in ~/.ssh/config
  wglink node add ct101 --pct 101 --via pve1 --endpoint 192.168.1.101
  wglink node add vm200 --qm 200 --via pve1 --endpoint 192.168.1.200
  wglink node add web --exec "docker exec -i web" --endpoint 172.17.0.2
  wglink node add laptop --local --endpoint 192.168.1.50
  wglink node add phone --manual --keepalive 25     # set up with wglink export`,
		Args: exactArgs(1, "a node name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !config.ValidNodeName(name) {
				return fmt.Errorf("%q: use lowercase letters, digits and hyphens, starting with a letter", name)
			}
			i, err := a.open()
			if err != nil {
				return err
			}
			if _, ok := i.inv.Nodes[name]; ok {
				return fmt.Errorf("node %s already exists; remove it first or edit %s", name, i.path)
			}
			var fields []config.Field
			switch {
			case f.ssh != "":
				fields = append(fields, config.Field{Key: "ssh", Value: f.ssh})
			case f.local:
				fields = append(fields, config.Field{Key: "local", Value: true})
			case f.exec != "":
				fields = append(fields, config.Field{Key: "exec", Value: config.Flow{V: strings.Fields(f.exec)}})
			case f.pct != 0 || f.qm != 0:
				kind, vmid := "pct", f.pct
				if f.qm != 0 {
					kind, vmid = "qm", f.qm
				}
				if f.via == "" {
					return fmt.Errorf("--%s needs --via, the Proxmox host's node name", kind)
				}
				fields = append(fields, config.Field{Key: kind, Value: config.Flow{V: map[string]any{"via": f.via, "vmid": vmid}}})
			case f.manual:
				m := map[string]any{}
				if f.pubkey != "" {
					if !wgconf.ValidKey(f.pubkey) {
						return fmt.Errorf("--pubkey: %q isn't a WireGuard public key", f.pubkey)
					}
					m["pubkey"] = f.pubkey
				}
				fields = append(fields, config.Field{Key: "manual", Value: config.Flow{V: m}})
				if f.endpoint == "" {
					f.endpoint = "none"
				}
			default:
				return errors.New("say how to reach it: --ssh, --local, --exec, --pct, --qm or --manual")
			}
			if f.pubkey != "" && !f.manual {
				return errors.New("--pubkey goes with --manual")
			}
			if f.via != "" && f.pct == 0 && f.qm == 0 {
				return errors.New("--via goes with --pct or --qm")
			}
			if f.sudo {
				fields = append(fields, config.Field{Key: "sudo", Value: true})
			}
			if f.endpoint != "" {
				fields = append(fields, config.Field{Key: "endpoint", Value: f.endpoint})
			}
			if f.keepalive != 0 {
				fields = append(fields, config.Field{Key: "keepalive", Value: f.keepalive})
			}
			if f.service != "" {
				fields = append(fields, config.Field{Key: "service", Value: f.service})
			}
			if err := i.doc.SetNode(name, fields...); err != nil {
				return err
			}
			if err := i.save(); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Added node %s.\n", name)
			switch {
			case f.manual:
				fmt.Fprintf(out, "Add it to a network, then run \"wglink export NETWORK %s\" for its config.\n", name)
			case !f.noCheck:
				check(cmd, i, name)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.ssh, "ssh", "", "reach it over SSH: user@host, ssh://user@host:port or a ~/.ssh/config alias")
	fl.BoolVar(&f.local, "local", false, "it is the machine wglink runs on")
	fl.StringVar(&f.exec, "exec", "", `reach it through a local command, e.g. "docker exec -i web"`)
	fl.IntVar(&f.pct, "pct", 0, "a Proxmox container ID, reached with pct exec through --via")
	fl.IntVar(&f.qm, "qm", 0, "a Proxmox VM ID, reached with its QEMU guest agent through --via")
	fl.StringVar(&f.via, "via", "", "the Proxmox host node for --pct or --qm")
	fl.BoolVar(&f.manual, "manual", false, "a device wglink can't reach, such as a phone; its config comes from wglink export")
	fl.StringVar(&f.pubkey, "pubkey", "", "the public key a --manual device already has (default: made by wglink export)")
	fl.BoolVar(&f.sudo, "sudo", false, "become root with sudo -n (ssh and local)")
	fl.StringVar(&f.endpoint, "endpoint", "", `address peers dial: host, host:port or "none" (default for ssh: the ssh host)`)
	fl.IntVar(&f.keepalive, "keepalive", 0, "send keepalives every N seconds (for nodes behind NAT)")
	fl.StringVar(&f.service, "service", "", "how tunnels start at boot: auto, systemd or none")
	fl.BoolVar(&f.noCheck, "no-check", false, "don't connect to check the node")
	cmd.MarkFlagsMutuallyExclusive("ssh", "local", "exec", "pct", "qm", "manual")
	return cmd
}

// check connects to a node and reports what wglink will need there.
func check(cmd *cobra.Command, i *inventory, name string) {
	out := cmd.OutOrStdout()
	inv, err := i.doc.Inventory()
	if err != nil {
		return
	}
	st, err := i.state()
	if err != nil {
		return
	}
	s := engine.New(i.dir, inv, st)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	facts, err := s.Check(ctx, name)
	if err != nil {
		fmt.Fprintf(out, "Couldn't reach %s yet (saved anyway): %v\n", name, err)
		return
	}
	var notes []string
	if facts.OS != "" {
		notes = append(notes, facts.OS)
	}
	if facts.Have["wg"] && facts.Have["wg-quick"] {
		notes = append(notes, "wireguard-tools installed")
	} else {
		notes = append(notes, "needs wireguard-tools: "+engine.InstallHint(facts.OS, "wireguard-tools"))
	}
	if facts.UID != 0 {
		notes = append(notes, "not root: use a root login or --sudo")
	}
	fmt.Fprintf(out, "%s is reachable (%s).\n", name, strings.Join(notes, "; "))
}

func (a *app) newNodeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List machines",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			var rows [][]string
			for _, name := range i.inv.NodeNames() {
				n := i.inv.Nodes[name]
				endpoint := "ssh host"
				if n.Endpoint != nil {
					endpoint = n.Endpoint.String()
				}
				rows = append(rows, []string{name, reach(n), endpoint, orDash(strings.Join(i.inv.NetworksOf(name), ","))})
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No nodes yet. Add one with: wglink node add NAME --ssh user@host")
				return nil
			}
			table(cmd.OutOrStdout(), "NAME REACH ENDPOINT NETWORKS", rows)
			return nil
		},
	}
}

func reach(n *config.Node) string {
	var s string
	switch n.Reach() {
	case config.ReachSSH:
		s = "ssh " + n.SSH
	case config.ReachLocal:
		s = "local"
	case config.ReachExec:
		s = "exec " + strings.Join(n.Exec, " ")
	case config.ReachPCT:
		s = fmt.Sprintf("pct %d via %s", n.PCT.VMID, n.PCT.Via)
	case config.ReachQM:
		s = fmt.Sprintf("qm %d via %s", n.QM.VMID, n.QM.Via)
	case config.ReachManual:
		s = "manual"
		if n.Manual.PubKey == "" {
			s += " (no key yet)"
		}
	}
	if n.Sudo {
		s += " (sudo)"
	}
	return s
}

func (a *app) newNodeRmCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"remove"},
		Short:   "Remove a machine from wglink.yaml",
		Long: `Remove a machine from wglink.yaml. To also delete its tunnels, take it out of
its networks and run "wglink apply" first; once it's removed, wglink can't
reach it to clean up.`,
		Args: exactArgs(1, "a node name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNode(name); err != nil {
				return err
			}
			for _, other := range i.inv.NodeNames() {
				if n := i.inv.Nodes[other]; n.PCT != nil && n.PCT.Via == name || n.QM != nil && n.QM.Via == name {
					return fmt.Errorf("%s is reached through %s; remove %s first", other, name, other)
				}
			}
			nets := i.inv.NetworksOf(name)
			if len(nets) > 0 && !force {
				return fmt.Errorf("%s is in %s; take it out first (wglink net leave) or use --force", name, strings.Join(nets, ", "))
			}
			for _, n := range nets {
				i.doc.RemoveMember(n, name)
			}
			i.doc.RemoveNode(name)
			if err := i.save(); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Removed node %s.\n", name)
			if st, err := i.state(); err == nil {
				for _, n := range i.inv.NetworkNames() {
					if sn := st.Networks[n]; sn != nil && sn.Members[name] != nil {
						fmt.Fprintf(out, "Note: %s still has the %s tunnel from the last apply; delete it there by hand.\n", name, n)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "also take the node out of its networks")
	return cmd
}
