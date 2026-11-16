// Package cli implements the wglink command line.
package cli

import (
	"github.com/spf13/cobra"
)

// Execute runs wglink with the command-line arguments.
func Execute() error {
	return newRoot(buildVersion()).Execute()
}

// app holds flags shared by every command.
type app struct {
	dir string
}

// newRoot builds a fresh command tree; commands keep parsed flags between runs.
func newRoot(version string) *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:   "wglink",
		Short: "Connect Linux machines over WireGuard",
		Long: `wglink connects Linux machines over WireGuard. Describe nodes and networks
in wglink.yaml, preview with "wglink plan", then "wglink apply". Keys are made
on the machines that use them, and nothing runs in the background.`,
		Version:      version,
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&a.dir, "dir", "", "directory holding wglink.yaml (default: $WGLINK_DIR, then ., then ~/.config/wglink)")
	root.AddGroup(&cobra.Group{ID: "inventory", Title: "Describe nodes and networks:"})
	root.AddCommand(
		newVersionCmd(),
		a.newInitCmd(),
		a.newNodeCmd(),
		a.newNetCmd(),
		a.newConnectCmd(),
		a.newIPCmd(),
	)
	return root
}
