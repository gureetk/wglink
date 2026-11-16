package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
)

func (a *app) newNetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "net",
		Aliases: []string{"network"},
		Short:   "Create, change and remove networks",
		GroupID: "inventory",
	}
	cmd.AddCommand(
		a.newNetCreateCmd(),
		a.newNetJoinCmd(),
		a.newNetLeaveCmd(),
		a.newNetListCmd(),
		a.newNetShowCmd(),
		a.newNetSetCmd(),
		a.newNetRmCmd(),
	)
	return cmd
}

// netFlags are the settings a network can be created or changed with.
type netFlags struct {
	subnet, port, mtu, keepalive, iface string
}

func (f *netFlags) add(cmd *cobra.Command, set bool) {
	auto := ""
	if set {
		auto = `, or "auto"`
	}
	fl := cmd.Flags()
	fl.StringVar(&f.subnet, "subnet", "", "IPv4 subnet, e.g. 10.99.0.0/24 (default: picked from the pool"+auto+")")
	fl.StringVar(&f.port, "port", "", "UDP listen port on every member (default: first free from 51820"+auto+")")
	fl.StringVar(&f.mtu, "mtu", "", "interface MTU (default: wg-quick decides"+auto+")")
	fl.StringVar(&f.keepalive, "keepalive", "", "keepalive interval in seconds for every member (0 is off)")
	if set {
		fl.StringVar(&f.iface, "iface", "", `interface name, or "auto" for wg-<network>`)
	}
}

// fields turns the flags into wglink.yaml edits; "auto" removes a setting.
func (f *netFlags) fields(cmd *cobra.Command) ([]config.Field, error) {
	var out []config.Field
	str := func(flag, key string, val string, check func(string) error) error {
		if !cmd.Flags().Changed(flag) {
			return nil
		}
		if val == "auto" {
			out = append(out, config.Field{Key: key})
			return nil
		}
		if err := check(val); err != nil {
			return fmt.Errorf("--%s: %w", flag, err)
		}
		out = append(out, config.Field{Key: key, Value: val})
		return nil
	}
	num := func(flag, key, val string, lo, hi int) error {
		if !cmd.Flags().Changed(flag) {
			return nil
		}
		if val == "auto" {
			out = append(out, config.Field{Key: key})
			return nil
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("--%s: want a number from %d to %d", flag, lo, hi)
		}
		if n == 0 {
			out = append(out, config.Field{Key: key})
			return nil
		}
		out = append(out, config.Field{Key: key, Value: n})
		return nil
	}
	checkSubnet := func(s string) error { _, err := config.ParseSubnet(s); return err }
	checkIface := func(s string) error { return nil } // validated with the whole inventory on save
	if err := str("subnet", "subnet", f.subnet, checkSubnet); err != nil {
		return nil, err
	}
	if err := num("port", "port", f.port, 1, 65535); err != nil {
		return nil, err
	}
	if err := num("mtu", "mtu", f.mtu, 1280, 9000); err != nil {
		return nil, err
	}
	if err := num("keepalive", "keepalive", f.keepalive, 0, 65535); err != nil {
		return nil, err
	}
	if err := str("iface", "iface", f.iface, checkIface); err != nil {
		return nil, err
	}
	return out, nil
}

// parsePins reads --ip NODE=SPEC flags.
func parsePins(pins []string) (map[string]config.AddrSpec, error) {
	out := map[string]config.AddrSpec{}
	for _, p := range pins {
		node, spec, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("--ip %q: want NODE=ADDRESS, e.g. nas=.10", p)
		}
		a, err := config.ParseAddrSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("--ip %s: %w", node, err)
		}
		out[node] = a
	}
	return out, nil
}

func (a *app) newConnectCmd() *cobra.Command {
	var f netFlags
	var name string
	var pins []string
	cmd := &cobra.Command{
		Use:   "connect NODE NODE [NODE...]",
		Short: "Join nodes in a network, creating it if needed",
		Long: `Join nodes in a full mesh. Without --name, two nodes get a network named after
them and more get net1, net2, and so on. Addresses are automatic unless pinned
with --ip, as a host part (nas=.10) or a full address (nas=10.99.0.10).`,
		Example: `  wglink connect pve1 pve2
  wglink connect pve1 nas ct101 --name lab
  wglink connect nas pve1 --name backup --subnet 172.30.5.0/24 --ip nas=.10`,
		GroupID: "inventory",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			for _, n := range args {
				if err := i.needNode(n); err != nil {
					return err
				}
			}
			pinned, err := parsePins(pins)
			if err != nil {
				return err
			}
			for n := range pinned {
				if !contains(args, n) {
					return fmt.Errorf("--ip %s: %s isn't one of the nodes being connected", n, n)
				}
			}
			if name == "" {
				name = autoName(i.inv, args)
			}
			fields, err := f.fields(cmd)
			if err != nil {
				return err
			}
			if _, exists := i.inv.Networks[name]; exists {
				if len(fields) > 0 {
					if err := i.doc.SetNetworkFields(name, fields...); err != nil {
						return err
					}
				}
			} else if err := i.doc.AddNetwork(name, fields...); err != nil {
				return err
			}
			for _, n := range args {
				if err := i.doc.SetMember(name, n, pinned[n]); err != nil {
					return err
				}
			}
			if err := i.save(); err != nil {
				return err
			}
			inv, _ := i.doc.Inventory()
			var names []string
			for _, m := range inv.Networks[name].Members {
				names = append(names, m.Name)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Network %s: %s.\n", name, strings.Join(names, ", "))
			fmt.Fprintln(out, `Run "wglink plan" to see what will change.`)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "network name, at most 12 characters")
	cmd.Flags().StringArrayVar(&pins, "ip", nil, "pin an address: NODE=.N, NODE=.N.N or NODE=ADDRESS (repeatable)")
	f.add(cmd, false)
	return cmd
}

// autoName names a new network after two nodes, or net1, net2...
func autoName(inv *config.Inventory, nodes []string) string {
	if len(nodes) == 2 {
		if n := nodes[0] + "-" + nodes[1]; config.ValidNetworkName(n) {
			if _, taken := inv.Networks[n]; !taken {
				return n
			}
		}
	}
	for k := 1; ; k++ {
		n := "net" + strconv.Itoa(k)
		if _, taken := inv.Networks[n]; !taken {
			return n
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (a *app) newNetCreateCmd() *cobra.Command {
	var f netFlags
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create an empty network",
		Args:  exactArgs(1, "a network name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !config.ValidNetworkName(args[0]) {
				return fmt.Errorf("%q: use lowercase letters, digits and hyphens, starting with a letter, at most 12 long", args[0])
			}
			i, err := a.open()
			if err != nil {
				return err
			}
			fields, err := f.fields(cmd)
			if err != nil {
				return err
			}
			if err := i.doc.AddNetwork(args[0], fields...); err != nil {
				return err
			}
			if err := i.save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created network %s. Add members with: wglink net join %s NODE...\n", args[0], args[0])
			return nil
		},
	}
	f.add(cmd, false)
	return cmd
}

func (a *app) newNetJoinCmd() *cobra.Command {
	var ip string
	cmd := &cobra.Command{
		Use:   "join NETWORK NODE [NODE...]",
		Short: "Add nodes to a network",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNetwork(args[0]); err != nil {
				return err
			}
			var spec config.AddrSpec
			if ip != "" {
				if len(args) != 2 {
					return errors.New("--ip applies to a single node")
				}
				if spec, err = config.ParseAddrSpec(ip); err != nil {
					return err
				}
			}
			for _, n := range args[1:] {
				if err := i.needNode(n); err != nil {
					return err
				}
				if err := i.doc.SetMember(args[0], n, spec); err != nil {
					return err
				}
			}
			if err := i.save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s to %s.\n", strings.Join(args[1:], ", "), args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&ip, "ip", "", "pin the address: .N, .N.N or a full address")
	return cmd
}

func (a *app) newNetLeaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "leave NETWORK NODE [NODE...]",
		Short: "Take nodes out of a network",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNetwork(args[0]); err != nil {
				return err
			}
			for _, n := range args[1:] {
				if !i.doc.RemoveMember(args[0], n) {
					return fmt.Errorf("%s isn't in %s", n, args[0])
				}
			}
			if err := i.save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Took %s out of %s. \"wglink apply\" removes the tunnels.\n", strings.Join(args[1:], ", "), args[0])
			return nil
		},
	}
}

func (a *app) newNetListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List networks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			st, err := i.state()
			if err != nil {
				return err
			}
			var rows [][]string
			for _, name := range i.inv.NetworkNames() {
				n := i.inv.Networks[name]
				subnet, port := "auto", "auto"
				if sn := st.Networks[name]; sn != nil {
					if sn.Subnet.IsValid() {
						subnet = sn.Subnet.String()
					}
					if sn.Port != 0 {
						port = strconv.Itoa(int(sn.Port))
					}
				}
				if !n.Subnet.Auto() {
					subnet = n.Subnet.String()
				}
				if n.Port != 0 {
					port = strconv.Itoa(n.Port)
				}
				var names []string
				for _, m := range n.Members {
					names = append(names, m.Name)
				}
				rows = append(rows, []string{name, n.IfaceName(), subnet, port, orDash(strings.Join(names, ","))})
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No networks yet. Create one with: wglink connect NODE NODE")
				return nil
			}
			table(cmd.OutOrStdout(), "NAME IFACE SUBNET PORT MEMBERS", rows)
			return nil
		},
	}
}

func (a *app) newNetShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show NETWORK",
		Short: "Show a network's settings and member addresses",
		Args:  exactArgs(1, "a network name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNetwork(args[0]); err != nil {
				return err
			}
			st, err := i.state()
			if err != nil {
				return err
			}
			n := i.inv.Networks[args[0]]
			sn := st.Networks[args[0]]
			out := cmd.OutOrStdout()
			setting := func(label, fixed, picked string) {
				switch {
				case fixed != "":
					fmt.Fprintf(out, "%-10s %s (pinned)\n", label, fixed)
				case picked != "":
					fmt.Fprintf(out, "%-10s %s (picked)\n", label, picked)
				default:
					fmt.Fprintf(out, "%-10s auto (picked at the next apply)\n", label)
				}
			}
			fmt.Fprintf(out, "%-10s %s\n", "network", n.Name)
			fmt.Fprintf(out, "%-10s %s\n", "interface", n.IfaceName())
			var fixedSubnet, pickedSubnet, fixedPort, pickedPort string
			if !n.Subnet.Auto() {
				fixedSubnet = n.Subnet.String()
			}
			if n.Port != 0 {
				fixedPort = strconv.Itoa(n.Port)
			}
			if sn != nil {
				if sn.Subnet.IsValid() {
					pickedSubnet = sn.Subnet.String()
				}
				if sn.Port != 0 {
					pickedPort = strconv.Itoa(int(sn.Port))
				}
			}
			setting("subnet", fixedSubnet, pickedSubnet)
			setting("port", fixedPort, pickedPort)
			if n.MTU != 0 {
				fmt.Fprintf(out, "%-10s %d\n", "mtu", n.MTU)
			}
			if n.Keepalive != 0 {
				fmt.Fprintf(out, "%-10s %ds\n", "keepalive", n.Keepalive)
			}
			fmt.Fprintln(out)
			var rows [][]string
			for _, m := range n.Members {
				addr, how := "-", "auto"
				if m.Addr.Kind != config.AddrAuto {
					how = "pinned " + m.Addr.String()
				}
				if sn != nil && sn.Members[m.Name] != nil && sn.Members[m.Name].IP.IsValid() {
					addr = sn.Members[m.Name].IP.String()
				}
				rows = append(rows, []string{m.Name, addr, how})
			}
			table(out, "MEMBER ADDRESS CHOSEN", rows)
			return nil
		},
	}
}

func (a *app) newNetSetCmd() *cobra.Command {
	var f netFlags
	cmd := &cobra.Command{
		Use:   "set NETWORK",
		Short: "Change a network's settings",
		Example: `  wglink net set lab --subnet 10.50.0.0/24
  wglink net set lab --port auto --mtu 1380`,
		Args: exactArgs(1, "a network name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNetwork(args[0]); err != nil {
				return err
			}
			fields, err := f.fields(cmd)
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				return errors.New("nothing to change; see --help for the settings")
			}
			if err := i.doc.SetNetworkFields(args[0], fields...); err != nil {
				return err
			}
			if err := i.save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated %s. Run \"wglink plan\" to see what will change.\n", args[0])
			return nil
		},
	}
	f.add(cmd, true)
	return cmd
}

func (a *app) newNetRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm NETWORK",
		Aliases: []string{"remove"},
		Short:   "Remove a network; the next apply deletes its tunnels",
		Args:    exactArgs(1, "a network name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			if !i.doc.RemoveNetwork(args[0]) {
				return fmt.Errorf("no network named %s", args[0])
			}
			if err := i.save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed network %s. \"wglink apply\" deletes its tunnels.\n", args[0])
			return nil
		},
	}
}

func (a *app) newIPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ip NETWORK NODE ADDRESS",
		Short: "Pin a member's address, or set it back to auto",
		Example: `  wglink ip lab nas .10            # host part in the network's subnet
  wglink ip lab nas 10.99.0.10     # full address
  wglink ip lab nas auto           # let wglink pick again`,
		GroupID: "inventory",
		Args:    exactArgs(3, "a network, a node and an address"),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNetwork(args[0]); err != nil {
				return err
			}
			if _, ok := i.inv.Networks[args[0]].Member(args[1]); !ok {
				return fmt.Errorf("%s isn't in %s; add it with: wglink net join %s %s", args[1], args[0], args[0], args[1])
			}
			spec, err := config.ParseAddrSpec(args[2])
			if err != nil {
				return err
			}
			if err := i.doc.SetMember(args[0], args[1], spec); err != nil {
				return err
			}
			if err := i.save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s in %s: %s. Run \"wglink plan\" to see what will change.\n", args[1], args[0], spec)
			return nil
		},
	}
}
