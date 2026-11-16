// Package cli implements the wglink command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Main runs wglink with os.Args and returns the exit code.
func Main() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRoot(buildVersion())
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var ec *exitCode
	if errors.As(err, &ec) {
		return ec.code
	}
	fmt.Fprintln(os.Stderr, "wglink:", err)
	return 1
}

// exitCode ends wglink with a code and no message, when output already explains why.
type exitCode struct{ code int }

func (e *exitCode) Error() string { return fmt.Sprintf("exit status %d", e.code) }

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
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.dir, "dir", "", "directory holding wglink.yaml (default: $WGLINK_DIR, then ., then ~/.config/wglink)")
	root.AddGroup(
		&cobra.Group{ID: "inventory", Title: "Describe nodes and networks:"},
		&cobra.Group{ID: "run", Title: "Change and inspect nodes:"},
	)
	root.AddCommand(
		newVersionCmd(),
		a.newInitCmd(),
		a.newNodeCmd(),
		a.newNetCmd(),
		a.newConnectCmd(),
		a.newIPCmd(),
		a.newDiscoverCmd(),
		a.newPlanCmd(),
		a.newApplyCmd(),
		a.newStatusCmd(),
		a.newExportCmd(),
		a.newStateCmd(),
	)
	return root
}
