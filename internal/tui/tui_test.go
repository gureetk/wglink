package tui

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/engine"
)

// fake is a Backend with canned status and plans, and a real inventory in a
// temporary directory for the forms.
type fake struct {
	Backend
	report  *engine.Report
	plan    *engine.Plan
	planned []string
	applied []string
}

func newFake(t *testing.T, yaml string) *fake {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return &fake{Backend: Engine(dir), report: &engine.Report{}}
}

func (f *fake) Status(context.Context) (*engine.Report, error) { return f.report, nil }

func (f *fake) Plan(_ context.Context, network string) (*Planned, error) {
	f.planned = append(f.planned, network)
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

const inventory = `version: 1
nodes:
  pve1: {ssh: root@pve1}
  nas: {ssh: admin@nas}
  vps: {ssh: root@vps}
networks:
  lab: {members: [pve1, nas]}
`

// typeText types s into the focused field.
func typeText(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m, _ = m.Update(char(r))
	}
	return m
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func TestAddNodeForm(t *testing.T) {
	f := newFake(t, inventory)
	var m tea.Model = New(context.Background(), f, "x", time.Minute, false)
	m, _ = m.Update(statusMsg{r: f.report, at: time.Now()})
	if !strings.Contains(text(m), "Press n to add a node") {
		t.Errorf("empty dashboard:\n%s", text(m))
	}
	m, cmd := press(t, m, char('n'))
	m = run(t, m, cmd)
	if out := text(m); !strings.Contains(out, "Add a node") || !strings.Contains(out, "‹ ssh ›") || strings.Contains(out, "VM ID") {
		t.Fatalf("form:\n%s", out)
	}
	m = typeText(m, "ct101")
	m, _ = press(t, m, key(tea.KeyTab))
	m, _ = press(t, m, key(tea.KeyRight))
	m, _ = press(t, m, key(tea.KeyRight))
	if out := text(m); !strings.Contains(out, "‹ pct ›") || !strings.Contains(out, "VM ID") || strings.Contains(out, "SSH  ") {
		t.Errorf("after picking pct:\n%s", out)
	}
	m, _ = press(t, m, key(tea.KeyTab))
	m = typeText(m, "101")
	m, _ = press(t, m, key(tea.KeyTab)) // the host: nas, the first that can host it
	m, _ = press(t, m, key(tea.KeyRight))
	m, _ = press(t, m, key(tea.KeyTab))
	m = typeText(m, "192.168.1.101")
	m, cmd = press(t, m, key(tea.KeyEnter))
	m = run(t, m, cmd)
	if out := text(m); m.(Model).screen != dashboard || !strings.Contains(out, "Added node ct101. Press c to connect it") {
		t.Errorf("after adding:\n%s", out)
	}
	inv, _ := f.Inventory()
	if n := inv.Nodes["ct101"]; n == nil || n.PCT == nil || n.PCT.VMID != 101 || n.PCT.Via != "pve1" || n.Endpoint.String() != "192.168.1.101" {
		t.Errorf("ct101 = %+v", n)
	}

	// Mistakes stay in the form, with what's wrong.
	m, cmd = press(t, m, char('n'))
	m = run(t, m, cmd)
	m = typeText(m, "nas")
	m, _ = press(t, m, key(tea.KeyEnter))
	if !strings.Contains(text(m), "Say where to SSH to") {
		t.Errorf("no SSH destination:\n%s", text(m))
	}
	m, _ = press(t, m, key(tea.KeyTab))
	m, _ = press(t, m, key(tea.KeyTab))
	m = typeText(m, "admin@nas2")
	m, cmd = press(t, m, key(tea.KeyEnter))
	m = run(t, m, cmd)
	if out := text(m); m.(Model).screen != addingNode || !strings.Contains(out, "node nas already exists") {
		t.Errorf("duplicate name:\n%s", out)
	}
	m, _ = press(t, m, key(tea.KeyEscape))
	if m.(Model).screen != dashboard {
		t.Error("esc didn't close the form")
	}
}

func TestConnectForm(t *testing.T) {
	f := newFake(t, inventory)
	f.plan = &engine.Plan{}
	var m tea.Model = New(context.Background(), f, "x", time.Minute, false)
	m, _ = m.Update(statusMsg{r: f.report, at: time.Now()})
	m, cmd := press(t, m, char('c'))
	m = run(t, m, cmd)
	if out := text(m); !strings.Contains(out, "Connect nodes") || !strings.Contains(out, "[ ] vps") || !strings.Contains(out, "Preshared") {
		t.Fatalf("form:\n%s", out)
	}
	// Fields: network, then nas, pve1 and vps, then hub, IPv6 and preshared keys.
	m, _ = press(t, m, key(tea.KeyTab))
	m, _ = press(t, m, key(tea.KeySpace))
	m, _ = press(t, m, key(tea.KeyTab))
	m, _ = press(t, m, key(tea.KeyTab))
	m, _ = press(t, m, key(tea.KeySpace))
	for range 3 {
		m, _ = press(t, m, key(tea.KeyTab))
	}
	m, _ = press(t, m, key(tea.KeySpace))
	m, cmd = press(t, m, key(tea.KeyEnter))
	m, cmd = m.Update(cmd()) // connected, so it plans the network
	if !strings.Contains(text(m), "Planning nas-vps") {
		t.Errorf("after connecting:\n%s", text(m))
	}
	m = run(t, m, cmd)
	if m.(Model).screen != reviewing || strings.Join(f.planned, ",") != "nas-vps" {
		t.Fatalf("after planning: screen %d, planned %v\n%s", m.(Model).screen, f.planned, text(m))
	}
	inv, _ := f.Inventory()
	if n := inv.Networks["nas-vps"]; n == nil || !n.PSK || len(n.Members) != 2 {
		t.Errorf("nas-vps = %+v", n)
	}

	// Naming a network there is joins it, and leaves its settings alone.
	m, _ = press(t, m, key(tea.KeyEscape))
	m, cmd = press(t, m, char('c'))
	m = run(t, m, cmd)
	m = typeText(m, "lab")
	if out := text(m); !strings.Contains(out, "joining lab; its settings stay") || strings.Contains(out, "Preshared") {
		t.Errorf("joining:\n%s", out)
	}
	m, _ = press(t, m, key(tea.KeyEnter))
	if !strings.Contains(text(m), "Check the nodes to connect") {
		t.Errorf("nothing checked:\n%s", text(m))
	}
	for range 3 {
		m, _ = press(t, m, key(tea.KeyTab))
	}
	m, _ = press(t, m, key(tea.KeySpace))
	m, cmd = press(t, m, key(tea.KeyEnter))
	run(t, m, cmd)
	inv, _ = f.Inventory()
	if _, ok := inv.Networks["lab"].Member("vps"); !ok || inv.Networks["lab"].PSK {
		t.Errorf("lab = %+v", inv.Networks["lab"])
	}
}
