package cli

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) newRoutesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "routes NETWORK NODE [SUBNET...]",
		Short: "Set the subnets a member routes for the others",
		Long: `Set the subnets behind a member, such as its LAN, that the network's other
members reach through it. With no subnets, the member routes nothing.

The member needs IP forwarding on, and hosts on those subnets need a route
back to the network through it.`,
		Example: `  wglink routes lab nas 192.168.50.0/24
  wglink routes lab nas              # route nothing`,
		GroupID: "inventory",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			network, name := args[0], args[1]
			var routes []netip.Prefix
			for _, a := range args[2:] {
				p, err := netip.ParsePrefix(a)
				if err != nil {
					return fmt.Errorf("%q isn't a subnet like 192.168.50.0/24", a)
				}
				routes = append(routes, p)
			}
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNetwork(network); err != nil {
				return err
			}
			if _, ok := i.inv.Networks[network].Member(name); !ok {
				return fmt.Errorf("%s isn't in %s; add it with: wglink net join %s %s", name, network, network, name)
			}
			if err := i.doc.SetMemberRoutes(network, name, routes); err != nil {
				return err
			}
			if err := i.save(); err != nil {
				return err
			}
			what := "nothing"
			if len(routes) > 0 {
				what = strings.Join(args[2:], ", ")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s routes %s in %s. Run \"wglink plan\" to see what will change.\n", name, what, network)
			return nil
		},
	}
}
