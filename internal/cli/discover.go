package cli

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/engine"
	"github.com/gureetk/wglink/internal/node"
)

func (a *app) newDiscoverCmd() *cobra.Command {
	var add bool
	cmd := &cobra.Command{
		Use:   "discover HOST",
		Short: "List a Proxmox host's containers and VMs, and add them as nodes",
		Long: `Ask a Proxmox VE host, already a node, for its containers and VMs, with the
addresses of the running ones. Containers are reached with pct and VMs with
the QEMU guest agent, both through the host, so guests need no SSH.

With --add, every running guest that isn't a node yet becomes one, named after
the guest and dialed at its address. Then add them to a network.`,
		Example: `  wglink discover pve1
  wglink discover pve1 --add
  wglink net join lab nas web`,
		GroupID: "inventory",
		Args:    exactArgs(1, "a Proxmox host node"),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := args[0]
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNode(host); err != nil {
				return err
			}
			switch i.inv.Nodes[host].Reach() {
			case config.ReachSSH, config.ReachLocal, config.ReachExec:
			default:
				return fmt.Errorf("%s must be an ssh, local or exec node to reach its guests through", host)
			}
			st, err := i.state()
			if err != nil {
				return err
			}
			nd, err := engine.New(i.dir, i.inv, st).Node(host)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancel()
			_, guests, err := nd.Discover(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(guests) == 0 {
				fmt.Fprintf(out, "%s has no containers or VMs.\n", host)
				return nil
			}

			taken := map[string]bool{}
			for name := range i.inv.Nodes {
				taken[name] = true
			}
			var rows [][]string
			var added, skipped []string
			fresh := 0
			for _, g := range guests {
				addr := "-"
				if len(g.Addrs) > 0 {
					addr = g.Addrs[0].Addr().String()
				}
				name, state := existing(i.inv, host, g), ""
				switch {
				case name != "":
				case g.Status != "running":
					state = "-"
				case g.Err != "":
					state = g.Err
				case len(g.Addrs) == 0:
					state = "no address"
				default:
					name = guestName(g, taken)
					taken[name] = true
					state = name + " (new)"
					fresh++
					if add {
						kind := map[string]string{"lxc": "pct", "qemu": "qm"}[g.Type]
						if err := i.doc.SetNode(name,
							config.Field{Key: kind, Value: config.Flow{V: map[string]any{"via": host, "vmid": g.VMID}}},
							config.Field{Key: "endpoint", Value: addr}); err != nil {
							return err
						}
						added = append(added, name)
						state = name + " (added)"
					}
				}
				if name != "" && state == "" {
					state = name
				}
				if add && g.Status == "running" && name == "" {
					skipped = append(skipped, fmt.Sprintf("%s %d (%s)", g.Name, g.VMID, state))
				}
				rows = append(rows, []string{strconv.Itoa(g.VMID), g.Type, orDash(g.Name), g.Status, addr, state})
			}
			if len(added) > 0 {
				if err := i.save(); err != nil {
					return err
				}
			}
			table(out, "VMID TYPE NAME STATUS ADDRESS NODE", rows)
			fmt.Fprintln(out)
			switch {
			case len(added) > 0:
				fmt.Fprintf(out, "Added %s. Next, put them in a network: wglink net join NETWORK %s\n", strings.Join(added, ", "), strings.Join(added, " "))
			case fresh > 0:
				fmt.Fprintf(out, "Run \"wglink discover %s --add\" to add the new ones as nodes.\n", host)
			default:
				fmt.Fprintln(out, "Nothing new to add.")
			}
			if len(skipped) > 0 {
				fmt.Fprintf(out, "Left out %s; add %s by hand, for example with --ssh.\n", strings.Join(skipped, ", "), plural(len(skipped), "it", "them"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&add, "add", false, "add every running guest that isn't a node yet")
	return cmd
}

// existing returns the node that already reaches guest g through host.
func existing(inv *config.Inventory, host string, g node.Guest) string {
	for _, name := range inv.NodeNames() {
		n := inv.Nodes[name]
		ref := n.PCT
		if g.Type == "qemu" {
			ref = n.QM
		}
		if ref != nil && ref.Via == host && ref.VMID == g.VMID {
			return name
		}
	}
	return ""
}

var notNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// guestName turns a guest's name into a free node name: lowercased, with
// other characters as hyphens. The VM ID makes it unique, or stands in for
// a name that won't do.
func guestName(g node.Guest, taken map[string]bool) string {
	name := strings.Trim(notNameChars.ReplaceAllString(strings.ToLower(g.Name), "-"), "-")
	id := strconv.Itoa(g.VMID)
	if !config.ValidNodeName(name) {
		name = map[string]string{"lxc": "ct", "qemu": "vm"}[g.Type] + id
	}
	if taken[name] {
		name = strings.TrimRight(name[:min(len(name), 31-len(id))], "-") + "-" + id
	}
	return name
}
