package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/edit"
	"github.com/gureetk/wglink/internal/state"
)

// inventory is wglink.yaml opened for reading or editing.
type inventory struct {
	dir  string
	path string
	doc  *config.Doc
	inv  *config.Inventory
}

// open finds and loads the inventory.
func (a *app) open() (*inventory, error) {
	dir, err := config.Find(a.dir)
	if err != nil {
		return nil, err
	}
	path, doc, inv, err := edit.Load(dir)
	if err != nil {
		return nil, err
	}
	return &inventory{dir: dir, path: path, doc: doc, inv: inv}, nil
}

// save validates and writes the edited inventory.
func (i *inventory) save() error { return i.doc.Save(i.path) }

// state loads the state file beside the inventory.
func (i *inventory) state() (*state.State, error) { return state.Load(i.dir) }

func (i *inventory) needNode(name string) error {
	if _, ok := i.inv.Nodes[name]; !ok {
		return fmt.Errorf("no node named %s (see \"wglink node list\")", name)
	}
	return nil
}

func (i *inventory) needNetwork(name string) error {
	if _, ok := i.inv.Networks[name]; !ok {
		return fmt.Errorf("no network named %s (see \"wglink net list\")", name)
	}
	return nil
}

// table prints aligned columns.
func table(w io.Writer, header string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, strings.ReplaceAll(header, " ", "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// exactArgs is cobra.ExactArgs with the expected words in the message.
func exactArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf("want %s, got %d %s", what, len(args), plural(len(args), "argument", "arguments"))
		}
		return nil
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
