package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/atomicfile"
	"github.com/gureetk/wglink/internal/engine"
	"github.com/gureetk/wglink/internal/state"
)

func (a *app) newStateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "state",
		Short:   "Look after wglink.state.json",
		GroupID: "run",
	}
	cmd.AddCommand(a.newStateRebuildCmd())
	return cmd
}

func (a *app) newStateRebuildCmd() *cobra.Command {
	var nets []string
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Recover wglink.state.json from the configs on the nodes",
		Long: `Read each member's config and put wglink.state.json back together: every
network's subnet and port, and every member's address, public key and config
hash. Use it when the state file is lost or out of date. The old file is kept
as wglink.state.json.bak.

Members wglink can't read, such as manual or unreachable ones, are taken from
their peers' configs, and manual nodes are assumed to have their current
config.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.session()
			if err != nil {
				return err
			}
			path := filepath.Join(s.Dir, state.FileName)
			data, err := os.ReadFile(path)
			switch {
			case err == nil:
				if err := atomicfile.Write(path+".bak", data, 0o644); err != nil {
					return err
				}
			case !errors.Is(err, fs.ErrNotExist):
				return err
			}
			got, err := s.Rebuild(cmd.Context(), nets)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			st := style(out)
			for _, r := range got {
				if !r.Subnet.IsValid() {
					fmt.Fprintf(out, "%s   %s   not set up on any member\n\n", st.Bold("network "+r.Name), r.Iface)
					continue
				}
				fmt.Fprintf(out, "%s   %s   %s   udp/%d\n", st.Bold("network "+r.Name), r.Iface, engine.Subnets(r.Subnet, r.Subnet6), r.Port)
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				for _, m := range r.Members {
					addr := "-"
					if m.Addr.IsValid() {
						addr = m.Addr.String()
					}
					fmt.Fprintf(tw, "  %s\t%s\t%s\n", m.Node, addr, m.From)
				}
				tw.Flush()
				for _, n := range r.Notes {
					fmt.Fprintf(out, "  %s %s\n", st.Yellow("note:"), n)
				}
				fmt.Fprintln(out)
			}
			saved := "Saved " + state.FileName
			if data != nil {
				saved += " (the old one is " + state.FileName + ".bak)"
			}
			fmt.Fprintf(out, "%s. Run \"wglink plan\" to check it against wglink.yaml.\n", saved)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&nets, "net", nil, "only these networks (repeatable)")
	return cmd
}
