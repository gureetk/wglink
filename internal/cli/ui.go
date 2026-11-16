package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/tui"
)

func (a *app) newUICmd() *cobra.Command {
	var every time.Duration
	cmd := &cobra.Command{
		Use:     "ui",
		Aliases: []string{"tui"},
		Short:   "Watch your networks live, and plan and apply from a dashboard",
		Long: `Open a dashboard in the terminal with every network and the health of each
link, checked again every few seconds. Pick a network to see its plan, and
apply it after a confirmation, the same as "wglink apply".`,
		GroupID: "run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := a.open()
			if err != nil {
				return err
			}
			if every < time.Second {
				return errors.New("--every: check at most once a second")
			}
			if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
				return errors.New(`the dashboard needs a terminal; use "wglink status" in scripts`)
			}
			title := filepath.Join(i.dir, config.FileName)
			if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(title, home+string(filepath.Separator)) {
				title = "~" + strings.TrimPrefix(title, home)
			}
			m := tui.New(cmd.Context(), tui.Engine(i.dir), title, every, style(os.Stdout).Color)
			_, err = tea.NewProgram(m, tea.WithContext(cmd.Context())).Run()
			if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().DurationVar(&every, "every", 10*time.Second, "how often to check the networks")
	return cmd
}
