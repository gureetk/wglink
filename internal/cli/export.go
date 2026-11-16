package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/engine"
	"github.com/gureetk/wglink/internal/wgconf"
)

func (a *app) newExportCmd() *cobra.Command {
	var output string
	var newKey bool
	cmd := &cobra.Command{
		Use:   "export NETWORK NODE",
		Short: "Print the config for a manual node, such as a phone",
		Long: `Print the wg-quick config for a manual node: a device wglink can't reach,
such as a phone. The first export makes the device's key pair. The private
key goes into the printed config and is kept nowhere else; the public key is
saved in wglink.yaml. Later exports leave a placeholder for the private key,
unless --new-key replaces the pair.

In a network with preshared keys, every export makes new ones: the peers
wglink can reach store theirs, and the config carries the device's.

Then run "wglink apply" so the other members add the device as a peer.
"wglink plan" says when a device's config has changed and needs exporting
again.`,
		Example: `  wglink node add phone --manual --keepalive 25
  wglink net join lab phone
  wglink export lab phone -o phone.conf
  qrencode -t ansiutf8 < phone.conf    # scan with the WireGuard app
  wglink apply`,
		GroupID: "run",
		Args:    exactArgs(2, "a network and a node"),
		RunE: func(cmd *cobra.Command, args []string) error {
			network, name := args[0], args[1]
			i, err := a.open()
			if err != nil {
				return err
			}
			if err := i.needNetwork(network); err != nil {
				return err
			}
			if err := i.needNode(name); err != nil {
				return err
			}
			n := i.inv.Nodes[name]
			if n.Manual == nil {
				return fmt.Errorf("%s isn't a manual node; wglink sets it up with apply", name)
			}
			if _, ok := i.inv.Networks[network].Member(name); !ok {
				return fmt.Errorf("%s isn't in %s; add it with: wglink net join %s %s", name, network, network, name)
			}
			errOut := cmd.ErrOrStderr()
			// Make sure the config has somewhere to go before making a key.
			var file *os.File
			if output != "" {
				if file, err = os.CreateTemp(filepath.Dir(output), ".wglink-export-*"); err != nil {
					return err
				}
				defer os.Remove(file.Name())
				defer file.Close()
			}

			s, err := engine.Open(i.dir)
			if err != nil {
				return err
			}
			private, pub := "", n.Manual.PubKey
			if pub == "" || newKey {
				if private, pub, err = wgconf.GenerateKey(); err != nil {
					return err
				}
				s.Inv.Nodes[name].Manual.PubKey = pub
			}
			ex, err := s.Export(cmd.Context(), network, name)
			if err != nil {
				if ex != nil && ex.Plan != nil {
					engine.WritePlan(errOut, ex.Plan, style(errOut))
				}
				return err
			}
			if private != "" {
				if err := i.doc.SetNodeFields(name, config.Field{Key: "manual", Value: config.Flow{V: map[string]any{"pubkey": pub}}}); err != nil {
					return err
				}
				if err := i.save(); err != nil {
					return err
				}
			}

			key := private
			if key == "" {
				key = fmt.Sprintf("(the private key from %s's first export)", name)
			}
			text := wgconf.WithPresharedKeys(wgconf.WithPrivateKey(ex.Config, key), ex.PSKs)
			if file == nil {
				if _, err := cmd.OutOrStdout().Write(text); err != nil {
					return err
				}
			} else if err := finishPrivate(file, text, output); err != nil {
				return err
			}

			switch {
			case private != "":
				fmt.Fprintf(errOut, "Made a new key pair for %s. Its private key is only in this config, so keep it safe.\n", name)
			default:
				fmt.Fprintf(errOut, "wglink doesn't keep %s's private key: fill in the one from its first export, or use --new-key.\n", name)
			}
			if len(ex.Missing) > 0 {
				fmt.Fprintf(errOut, "Left out %s, which %s no key yet; run \"wglink apply\", then export again.\n", strings.Join(ex.Missing, ", "), plural(len(ex.Missing), "has", "have"))
			}
			if len(ex.NoPSK) > 0 {
				fmt.Fprintf(errOut, "Couldn't reach %s to give %s a preshared key, so the config has none for %s; export again once %s.\n",
					strings.Join(ex.NoPSK, ", "), plural(len(ex.NoPSK), "it", "them"), plural(len(ex.NoPSK), "it", "them"), plural(len(ex.NoPSK), "it's reachable", "they're reachable"))
			}
			if len(ex.PSKs) > 0 {
				fmt.Fprintf(errOut, "Stored new preshared keys for %s on %s.\n", name, strings.Join(slices.Sorted(maps.Keys(ex.PSKs)), ", "))
			}
			if !ex.Plan.Empty() || private != "" || len(ex.PSKs) > 0 {
				fmt.Fprintf(errOut, "Run \"wglink apply\" so the other members of %s update their side.\n", network)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the config to this file (mode 0600) instead of stdout")
	cmd.Flags().BoolVar(&newKey, "new-key", false, "make a new key pair; the device needs the new config")
	return cmd
}

// finishPrivate writes data to a temp file made by os.CreateTemp, which only
// its owner can read, and moves it into place.
func finishPrivate(f *os.File, data []byte, path string) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
