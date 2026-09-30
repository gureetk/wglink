package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// newVersionCmd returns a command that prints the same line as --version.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the wglink version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := cmd.Root()
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", root.Name(), root.Version)
			return err
		},
	}
}

// buildVersion returns the version the go command stamped into the binary.
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
