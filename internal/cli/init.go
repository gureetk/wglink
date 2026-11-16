package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
)

func (a *app) newInitCmd() *cobra.Command {
	var pool string
	var prefix, port int
	cmd := &cobra.Command{
		Use:     "init",
		Short:   "Create wglink.yaml in the current directory or --dir",
		GroupID: "inventory",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := a.dir
			if dir == "" {
				dir = os.Getenv("WGLINK_DIR")
			}
			if dir == "" {
				dir = "."
			}
			p, err := netip.ParsePrefix(pool)
			if err != nil {
				return fmt.Errorf("--pool: %w", err)
			}
			path := filepath.Join(dir, config.FileName)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			data := config.Template(p, prefix, port)
			if _, err := config.Parse(path, data); err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			// Keep the lock file out of git if the inventory lives in a repo.
			ignore := filepath.Join(dir, ".gitignore")
			if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
				os.WriteFile(ignore, []byte(".wglink.state.json.lock\n"), 0o644)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Created %s.\n", path)
			fmt.Fprintln(out, "Next, add machines: wglink node add pve1 --ssh root@192.168.1.10")
			return nil
		},
	}
	cmd.Flags().StringVar(&pool, "pool", config.DefaultPool.String(), "range automatic subnets come from")
	cmd.Flags().IntVar(&prefix, "prefix", config.DefaultPrefix, "size of each automatic subnet")
	cmd.Flags().IntVar(&port, "port", config.DefaultPort, "first UDP port tried")
	return cmd
}
