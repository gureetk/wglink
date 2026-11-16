package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/engine"
)

// scope holds the flags that narrow plan, apply and status.
type scope struct {
	nets, nodes []string
}

func (s *scope) add(cmd *cobra.Command) {
	cmd.Flags().StringSliceVar(&s.nets, "net", nil, "only these networks (repeatable)")
	cmd.Flags().StringSliceVar(&s.nodes, "node", nil, "only change these nodes (repeatable)")
}

// session opens the inventory and state for running against nodes.
func (a *app) session() (*engine.Session, error) {
	dir, err := config.Find(a.dir)
	if err != nil {
		return nil, err
	}
	return engine.Open(dir)
}

func (a *app) newPlanCmd() *cobra.Command {
	var sc scope
	var adopt, exitOnChange bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what apply would change",
		Long: `Connect to every node involved and show what "wglink apply" would change.
Nothing is changed. Problems listed under "problems" stop apply until fixed.`,
		GroupID: "run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.session()
			if err != nil {
				return err
			}
			p, err := s.Plan(cmd.Context(), engine.Options{Networks: sc.nets, Nodes: sc.nodes, Adopt: adopt})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			engine.WritePlan(out, p, style(out))
			switch {
			case len(p.Problems) > 0:
				return &exitCode{1}
			case !p.Empty():
				if exitOnChange {
					return &exitCode{2}
				}
				fmt.Fprintln(out, `Run "wglink apply" to make these changes.`)
			}
			return nil
		},
	}
	sc.add(cmd)
	cmd.Flags().BoolVar(&adopt, "adopt", false, "replace configs wglink didn't write")
	cmd.Flags().BoolVar(&exitOnChange, "exit-code", false, "exit with status 2 when there are changes")
	return cmd
}

func (a *app) newApplyCmd() *cobra.Command {
	var sc scope
	var adopt, yes, noRevert bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Make the nodes match wglink.yaml",
		Long: `Show the plan, ask for confirmation, then change the nodes: remove interfaces
that are no longer wanted, create missing keys on their own nodes, and write
configs one node at a time. It stops at the first failure; running it again
picks up where it stopped.

Each change to an SSH node is confirmed over a new SSH connection. If that
fails, for example because the change cut off the route wglink uses, the
node puts its previous config back after a minute. This needs systemd on the
node; --no-revert turns it off.`,
		GroupID: "run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.apply(cmd, engine.Options{Networks: sc.nets, Nodes: sc.nodes, Adopt: adopt, NoRevert: noRevert}, yes)
		},
	}
	sc.add(cmd)
	cmd.Flags().BoolVar(&adopt, "adopt", false, "replace configs wglink didn't write")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&noRevert, "no-revert", false, "don't have SSH nodes undo a change that wglink can't confirm")
	return cmd
}

// apply shows the plan, asks unless yes, and makes the changes.
func (a *app) apply(cmd *cobra.Command, opts engine.Options, yes bool) error {
	s, err := a.session()
	if err != nil {
		return err
	}
	p, err := s.Plan(cmd.Context(), opts)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	st := style(out)
	engine.WritePlan(out, p, st)
	if len(p.Problems) > 0 {
		return &exitCode{1}
	}
	if p.Empty() {
		return nil
	}
	if !yes {
		ok, err := confirm(cmd.InOrStdin(), out, "Apply these changes?")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Nothing changed.")
			return nil
		}
	}
	fmt.Fprintln(out)
	err = s.Apply(cmd.Context(), p, func(format string, args ...any) {
		fmt.Fprintf(out, "  %s\n", fmt.Sprintf(format, args...))
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, st.Green("Done."))
	return nil
}

func (a *app) newRotateCmd() *cobra.Command {
	var yes, noRevert bool
	cmd := &cobra.Command{
		Use:   "rotate NETWORK [NODE...]",
		Short: "Replace members' keys",
		Long: `Make new keys for some or all of a network's members, on the members
themselves, and give the new public keys to their peers. Each member loads its
new key live. Manual nodes get new keys with "wglink export --new-key".`,
		Example: `  wglink rotate lab            # every member that wglink reaches
  wglink rotate lab nas`,
		GroupID: "run",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			network, nodes := args[0], args[1:]
			if len(nodes) == 0 {
				i, err := a.open()
				if err != nil {
					return err
				}
				if err := i.needNetwork(network); err != nil {
					return err
				}
				for _, m := range i.inv.Networks[network].Members {
					if i.inv.Nodes[m.Name].Manual == nil {
						nodes = append(nodes, m.Name)
					}
				}
			}
			return a.apply(cmd, engine.Options{Networks: []string{network}, Rotate: nodes, NoRevert: noRevert}, yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&noRevert, "no-revert", false, "don't have SSH nodes undo a change that wglink can't confirm")
	return cmd
}

// confirm asks a yes/no question on a terminal.
func confirm(in io.Reader, out io.Writer, question string) (bool, error) {
	if f, ok := in.(*os.File); ok && !isTerminal(f) {
		return false, errors.New("not asking for confirmation without a terminal; add --yes")
	}
	fmt.Fprintf(out, "\n%s [y/N] ", question)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// style colors output for terminals, unless NO_COLOR is set.
func style(w io.Writer) engine.Style {
	f, ok := w.(*os.File)
	return engine.Style{Color: ok && isTerminal(f) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
