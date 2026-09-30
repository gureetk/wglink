// Package cli implements the wglink command line.
package cli

import "github.com/spf13/cobra"

// Execute runs wglink with the command-line arguments.
func Execute() error {
	return newRoot(buildVersion()).Execute()
}

// newRoot builds a fresh command tree; commands keep parsed flags between runs.
func newRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:          "wglink",
		Short:        "Connect Linux machines over WireGuard",
		Version:      version,
		SilenceUsage: true,
	}
	root.AddCommand(newVersionCmd())
	return root
}
