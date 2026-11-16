package engine

import (
	"fmt"
	"io"
	"strings"
)

// Style colors terminal output; the zero value prints plain text.
type Style struct{ Color bool }

func (s Style) wrap(code, text string) string {
	if !s.Color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s Style) Bold(t string) string   { return s.wrap("1", t) }
func (s Style) Dim(t string) string    { return s.wrap("2", t) }
func (s Style) Green(t string) string  { return s.wrap("32", t) }
func (s Style) Yellow(t string) string { return s.wrap("33", t) }
func (s Style) Red(t string) string    { return s.wrap("31", t) }

// WritePlan prints a plan for people.
func WritePlan(w io.Writer, p *Plan, st Style) {
	for i, n := range p.Networks {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s   %s   %s   udp/%d\n", st.Bold("network "+n.Name), n.Iface, n.Subnet, n.Port)
		width := 0
		for _, m := range n.Members {
			width = max(width, len(m.Node))
		}
		for _, m := range n.Members {
			sym, text := memberLine(m)
			fmt.Fprintf(w, "  %s %-*s  %-15s %s\n", colorSym(st, sym), width, m.Node, addrOrDash(m), text)
		}
	}
	if len(p.Removals) > 0 {
		if len(p.Networks) > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, st.Bold("remove"))
		for _, r := range p.Removals {
			if r.Gone {
				fmt.Fprintf(w, "  %s %s %s   %s (forget only)\n", st.Dim("-"), r.Node, r.Iface, r.Reason)
				continue
			}
			fmt.Fprintf(w, "  %s %s %s   %s\n", st.Red("-"), r.Node, r.Iface, r.Reason)
		}
	}
	if len(p.Warnings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.Yellow("warnings"))
		for _, msg := range p.Warnings {
			fmt.Fprintf(w, "  %s\n", indentRest(msg))
		}
	}
	if len(p.Problems) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.Red("problems (apply refuses until these are fixed)"))
		for _, msg := range p.Problems {
			fmt.Fprintf(w, "  %s\n", indentRest(msg))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, Summary(p))
}

// Summary is the one-line verdict under a plan.
func Summary(p *Plan) string {
	if len(p.Problems) > 0 {
		return fmt.Sprintf("%d %s to fix before apply.", len(p.Problems), plural(len(p.Problems), "problem", "problems"))
	}
	if p.Empty() {
		return "Everything is up to date."
	}
	c, l, r, d := p.Changes()
	return fmt.Sprintf("%d to create, %d to change live, %d to restart, %d to remove.", c, l, r, d)
}

func memberLine(m *MemberPlan) (sym, text string) {
	why := strings.Join(m.Reasons, ", ")
	switch m.Action {
	case Create:
		return "+", why
	case Live:
		return "~", why + " (live)"
	case Restart:
		return "~", why + " (restart)"
	case Start:
		return "~", "start: " + why
	case Skip:
		return "?", why
	}
	return "=", "up to date"
}

func colorSym(st Style, sym string) string {
	switch sym {
	case "+":
		return st.Green(sym)
	case "~":
		return st.Yellow(sym)
	case "-":
		return st.Red(sym)
	}
	return st.Dim(sym)
}

func addrOrDash(m *MemberPlan) string {
	if m.Addr.IsValid() {
		return m.Addr.String()
	}
	return "-"
}

// indentRest keeps multi-line messages aligned under their first line.
func indentRest(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ") }
