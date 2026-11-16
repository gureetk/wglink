package tui

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/gureetk/wglink/internal/engine"
)

// fake is a Backend with canned answers.
type fake struct {
	report  *engine.Report
	plan    *engine.Plan
	applied []string
}

func (f *fake) Status(context.Context) (*engine.Report, error) { return f.report, nil }

func (f *fake) Plan(_ context.Context, network string) (*Planned, error) {
	if f.plan == nil {
		return nil, errors.New("no such network")
	}
	return &Planned{Plan: f.plan, Apply: func(_ context.Context, logf engine.Logf) error {
		f.applied = append(f.applied, network)
		logf("%s: updated live %s", "pve1", "wg-"+network)
		return nil
	}}, nil
}

func network(name string, state engine.LinkState) *engine.NetReport {
	return &engine.NetReport{
		Name: name, Iface: "wg-" + name, Subnet: netip.MustParsePrefix("10.99.0.0/24"), Port: 51820,
		Members: []*engine.MemberReport{
			{Node: "pve1", Addr: netip.MustParseAddr("10.99.0.1"), State: engine.MemberUp},
			{Node: "nas", Addr: netip.MustParseAddr("10.99.0.2"), State: engine.MemberUp},
		},
		Links: []*engine.Link{{A: "pve1", B: "nas", Health: engine.Health{State: state, Handshake: 5 * time.Second}}},
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func text(m tea.Model) string { return ansi.ReplaceAllString(m.View().Content, "") }

func press(t *testing.T, m tea.Model, key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t.Helper()
	return m.Update(key)
}

func char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// run executes a command and feeds its message back, as the program would.
func run(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command to run")
	}
	m, _ = m.Update(cmd())
	return m
}

func TestDashboard(t *testing.T) {
	f := &fake{report: &engine.Report{Networks: []*engine.NetReport{network("lab", engine.LinkOK), network("road", engine.LinkDown)}}}
	var m tea.Model = New(context.Background(), f, "~/wglink.yaml", time.Minute, false)
	if !strings.Contains(text(m), "Checking your networks") {
		t.Errorf("before the first check:\n%s", text(m))
	}
	m = run(t, m, Model{be: f, ctx: context.Background()}.check())
	out := text(m)
	for _, want := range []string{"NETWORKS", "✓ lab 1/1", "✗ road 0/1", "network lab", "pve1", "q quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard lacks %q:\n%s", want, out)
		}
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if out := text(m); !strings.Contains(out, "network road") || strings.Contains(out, "network lab") {
		t.Errorf("after down:\n%s", out)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.(Model).sel != 1 {
		t.Errorf("selection went past the end: %d", m.(Model).sel)
	}
	if _, cmd := press(t, m, char('q')); cmd == nil || cmd() != tea.Quit() {
		t.Error("q doesn't quit")
	}
}

func TestRefreshTimer(t *testing.T) {
	f := &fake{report: &engine.Report{}}
	var m tea.Model = New(context.Background(), f, "x", time.Minute, false)
	m, cmd := m.Update(statusMsg{r: f.report, at: time.Now()})
	if cmd == nil || m.(Model).checking {
		t.Fatal("no timer after a check")
	}
	if !strings.Contains(text(m), "No networks yet") {
		t.Errorf("empty inventory:\n%s", text(m))
	}
	id := m.(Model).tick
	if _, cmd := m.Update(tickMsg{id - 1}); cmd != nil {
		t.Error("a stale timer started a check")
	}
	m, cmd = m.Update(tickMsg{id})
	if cmd == nil || !m.(Model).checking {
		t.Error("the timer didn't start a check")
	}
	if _, cmd := press(t, m, char('r')); cmd != nil {
		t.Error("r started a second check while one runs")
	}
}

func TestPlanAndApply(t *testing.T) {
	plan := &engine.Plan{Networks: []*engine.NetPlan{{
		Name: "lab", Iface: "wg-lab", Subnet: netip.MustParsePrefix("10.99.0.0/24"), Port: 51820,
		Members: []*engine.MemberPlan{{Node: "pve1", Addr: netip.MustParseAddr("10.99.0.1"), Action: engine.Live, Reasons: []string{"add peer nas"}}},
	}}}
	f := &fake{report: &engine.Report{Networks: []*engine.NetReport{network("lab", engine.LinkOK)}}, plan: plan}
	var m tea.Model = New(context.Background(), f, "x", time.Minute, false)
	m, _ = m.Update(statusMsg{r: f.report, at: time.Now()})

	m, cmd := press(t, m, char('p'))
	if !strings.Contains(text(m), "Planning lab") {
		t.Errorf("planning:\n%s", text(m))
	}
	m = run(t, m, cmd)
	if out := text(m); !strings.Contains(out, "add peer nas") || !strings.Contains(out, "a apply") {
		t.Errorf("plan:\n%s", out)
	}
	m, _ = press(t, m, char('a'))
	if out := text(m); !strings.Contains(out, "Apply these changes to lab? y/n") {
		t.Errorf("confirm:\n%s", out)
	}
	m, _ = press(t, m, char('n'))
	m, _ = press(t, m, char('a'))
	m, cmd = press(t, m, char('y'))
	if !strings.Contains(text(m), "Applying lab") {
		t.Errorf("applying:\n%s", text(m))
	}
	if _, quit := press(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); quit != nil {
		t.Error("ctrl+c quit in the middle of an apply")
	}
	m = run(t, m, cmd)
	if out := text(m); !strings.Contains(out, "pve1: updated live wg-lab") || !strings.Contains(out, "Done.") || len(f.applied) != 1 {
		t.Errorf("after apply (%v):\n%s", f.applied, out)
	}
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.(Model).screen != dashboard || cmd == nil {
		t.Error("a key after apply doesn't go back and check again")
	}

	// A plan with problems can't be applied, and a failed plan says why.
	plan.Problems = []string{"nas: unreachable"}
	m, cmd = press(t, m, char('p'))
	m = run(t, m, cmd)
	m, _ = press(t, m, char('a'))
	if m.(Model).screen != reviewing || !strings.Contains(text(m), "Fix the problems first") {
		t.Errorf("applied a plan with problems:\n%s", text(m))
	}
	f.plan = nil
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, cmd = press(t, m, char('p'))
	m = run(t, m, cmd)
	if out := text(m); !strings.Contains(out, "Couldn't plan lab: no such network") {
		t.Errorf("failed plan:\n%s", out)
	}
}
