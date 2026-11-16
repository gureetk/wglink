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

// WriteStatus prints a status report for people.
func WriteStatus(w io.Writer, r *Report, st Style) {
	if len(r.Networks) == 0 {
		fmt.Fprintln(w, "No networks yet. Connect two nodes with: wglink connect NODE NODE")
		return
	}
	for i, n := range r.Networks {
		if i > 0 {
			fmt.Fprintln(w)
		}
		where := "not applied yet"
		if n.Subnet.IsValid() {
			where = fmt.Sprintf("%s   udp/%d", n.Subnet, n.Port)
		}
		fmt.Fprintf(w, "%s   %s   %s\n", st.Bold("network "+n.Name), n.Iface, where)
		width := 0
		for _, m := range n.Members {
			width = max(width, len(m.Node))
		}
		for _, m := range n.Members {
			addr := "-"
			if m.Addr.IsValid() {
				addr = m.Addr.String()
			}
			fmt.Fprintf(w, "  %-*s  %-15s %s\n", width, m.Node, addr, memberStatus(st, m))
		}
		if len(n.Links) == 0 {
			continue
		}
		fmt.Fprintln(w)
		var rows [][3]string
		var states []LinkState
		for _, l := range n.Links {
			rows = append(rows, [3]string{l.A + " <-> " + l.B, l.Note, traffic(l.Health)})
			states = append(states, l.State)
		}
		writeHealth(w, rows, states, st)
	}
	if len(r.Errors) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.Red("unreachable"))
		for _, name := range keys(r.Errors) {
			fmt.Fprintf(w, "  %s: %s\n", name, indentRest(r.Errors[name].Error()))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, StatusSummary(r))
}

// StatusSummary is the one-line verdict under a status report.
func StatusSummary(r *Report) string {
	var states []LinkState
	for _, n := range r.Networks {
		for _, l := range n.Links {
			states = append(states, l.State)
		}
	}
	s := "No links yet: a network needs two members."
	if len(states) > 0 {
		s = summarize(states, "link", "links")
	}
	if len(r.Errors) > 0 {
		s += fmt.Sprintf(" Couldn't reach %s.", strings.Join(keys(r.Errors), ", "))
	}
	return s
}

// AllOK reports whether every link is ok.
func (r *Report) AllOK() bool {
	for _, n := range r.Networks {
		for _, l := range n.Links {
			if l.State != LinkOK {
				return false
			}
		}
	}
	return true
}

// WriteLocalStatus prints one machine's interfaces for people.
func WriteLocalStatus(w io.Writer, ifaces []*IfaceReport, st Style) {
	for i, r := range ifaces {
		if i > 0 {
			fmt.Fprintln(w)
		}
		head := []string{st.Bold("interface " + r.Name)}
		if r.Network != "" {
			head = append(head, "network "+r.Network)
		}
		for _, a := range r.Addresses {
			head = append(head, a.String())
		}
		if r.Port != 0 {
			head = append(head, fmt.Sprintf("udp/%d", r.Port))
		}
		if !r.Up {
			head = append(head, st.Red("down"))
		}
		fmt.Fprintln(w, strings.Join(head, "   "))
		if r.Up && len(r.Peers) == 0 {
			fmt.Fprintln(w, "  no peers")
		}
		var rows [][3]string
		var states []LinkState
		for _, p := range r.Peers {
			name := p.Name
			if name == "" {
				name = shortKey(p.PublicKey)
			}
			rows = append(rows, [3]string{name, p.Note, traffic(p.Health)})
			states = append(states, p.State)
		}
		writeHealth(w, rows, states, st)
	}
	if len(ifaces) > 0 {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, LocalSummary(ifaces))
}

// LocalSummary is the one-line verdict under a machine's interfaces.
func LocalSummary(ifaces []*IfaceReport) string {
	if len(ifaces) == 0 {
		return "No WireGuard interfaces here."
	}
	var states []LinkState
	down := 0
	for _, r := range ifaces {
		if !r.Up {
			down++
		}
		for _, p := range r.Peers {
			states = append(states, p.State)
		}
	}
	s := "No peers yet."
	if len(states) > 0 {
		s = summarize(states, "peer", "peers")
	}
	if down > 0 {
		s = fmt.Sprintf("%d %s down. %s", down, plural(down, "interface is", "interfaces are"), s)
	}
	return s
}

// LocalOK reports whether every interface is up and every peer ok.
func LocalOK(ifaces []*IfaceReport) bool {
	for _, r := range ifaces {
		if !r.Up {
			return false
		}
		for _, p := range r.Peers {
			if p.State != LinkOK {
				return false
			}
		}
	}
	return true
}

// writeHealth prints rows of name, state, note and traffic in columns.
func writeHealth(w io.Writer, rows [][3]string, states []LinkState, st Style) {
	nameW, stateW, noteW := 0, 0, 0
	for i, r := range rows {
		nameW = max(nameW, len(r[0]))
		stateW = max(stateW, len(states[i]))
		if r[2] != "" {
			noteW = max(noteW, len(r[1]))
		}
	}
	for i, r := range rows {
		s := string(states[i])
		line := fmt.Sprintf("  %-*s  %s%s  %s", nameW, r[0], colorState(st, states[i]), strings.Repeat(" ", stateW-len(s)), r[1])
		if r[2] != "" {
			line += strings.Repeat(" ", noteW-len(r[1])) + "  " + r[2]
		}
		fmt.Fprintln(w, line)
	}
}

func memberStatus(st Style, m *MemberReport) string {
	var s string
	switch m.State {
	case MemberUp:
		s = st.Green("up")
	case MemberDown:
		s = st.Red("down")
	case MemberNew:
		s = st.Yellow("not applied yet")
	case MemberUnreachable:
		s = st.Red("unreachable")
	}
	if m.Drift {
		s += ", " + st.Yellow("edited on the node since the last apply")
	}
	return s
}

func colorState(st Style, s LinkState) string {
	switch s {
	case LinkOK:
		return st.Green(string(s))
	case LinkIdle, LinkUnknown:
		return st.Yellow(string(s))
	}
	return st.Red(string(s))
}

// traffic shows bytes moved, once there are any.
func traffic(h Health) string {
	if h.RX == 0 && h.TX == 0 {
		return ""
	}
	return fmt.Sprintf("rx %s  tx %s", Bytes(h.RX), Bytes(h.TX))
}

// summarize counts states into one line, such as "3 links: 2 ok, 1 down."
func summarize(states []LinkState, one, many string) string {
	counts := map[LinkState]int{}
	for _, s := range states {
		counts[s]++
	}
	if counts[LinkOK] == len(states) {
		if len(states) == 1 {
			return "The " + one + " is ok."
		}
		return fmt.Sprintf("All %d %s are ok.", len(states), many)
	}
	var parts []string
	for _, s := range []LinkState{LinkOK, LinkIdle, LinkDown, LinkMissing, LinkUnknown} {
		if c := counts[s]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, s))
		}
	}
	return fmt.Sprintf("%d %s: %s.", len(states), plural(len(states), one, many), strings.Join(parts, ", "))
}

// shortKey abbreviates a public key for display.
func shortKey(k string) string {
	if len(k) > 10 {
		return k[:10] + "..."
	}
	return k
}

// Bytes formats a byte count, such as 1.2 MiB.
func Bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
