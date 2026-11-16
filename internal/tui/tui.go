// Package tui is wglink's terminal dashboard: the live state of every
// network, with plan and apply a key away.
package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/gureetk/wglink/internal/engine"
)

// Backend is what the dashboard drives: the engine, or a fake in tests.
type Backend interface {
	Status(ctx context.Context) (*engine.Report, error)
	Plan(ctx context.Context, network string) (*Planned, error)
}

// Planned is a plan and the way to apply it.
type Planned struct {
	*engine.Plan
	Apply func(ctx context.Context, logf engine.Logf) error
}

// Engine is the Backend for the inventory in dir. Each call reads the
// inventory and state again, so edits made meanwhile are picked up.
func Engine(dir string) Backend { return engineBackend(dir) }

type engineBackend string

func (dir engineBackend) Status(ctx context.Context) (*engine.Report, error) {
	s, err := engine.Open(string(dir))
	if err != nil {
		return nil, err
	}
	return s.Status(ctx, engine.StatusOptions{})
}

func (dir engineBackend) Plan(ctx context.Context, network string) (*Planned, error) {
	s, err := engine.Open(string(dir))
	if err != nil {
		return nil, err
	}
	p, err := s.Plan(ctx, engine.Options{Networks: []string{network}})
	if err != nil {
		return nil, err
	}
	return &Planned{Plan: p, Apply: func(ctx context.Context, logf engine.Logf) error { return s.Apply(ctx, p, logf) }}, nil
}

type screen int

const (
	dashboard  screen = iota
	planning          // waiting for a plan
	reviewing         // a plan on screen
	confirming        // asked whether to apply it
	applying
	done // what apply did, or why planning failed
)

// Model is the dashboard's state.
type Model struct {
	ctx   context.Context
	be    Backend
	title string
	every time.Duration
	style engine.Style

	width, height int
	screen        screen
	report        *engine.Report
	err           error     // from the last status check
	at            time.Time // when the report came in
	checking      bool
	tick          int // only the latest refresh timer counts
	sel           int // the selected network

	network string // the one being planned or applied
	plan    *Planned
	failed  bool
	vp      viewport.Model
	spin    spinner.Model
}

type (
	statusMsg struct {
		r   *engine.Report
		err error
		at  time.Time
	}
	tickMsg struct{ id int }
	planMsg struct {
		p   *Planned
		err error
	}
	applyMsg struct {
		log []string
		err error
	}
)

// New makes a dashboard for the inventory named title, checking it every
// so often.
func New(ctx context.Context, be Backend, title string, every time.Duration, color bool) Model {
	return Model{
		ctx: ctx, be: be, title: title, every: every, style: engine.Style{Color: color},
		width: 80, height: 24, checking: true,
		vp:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		spin: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

// Init starts the first check.
func (m Model) Init() tea.Cmd { return tea.Batch(m.spin.Tick, m.check()) }

func (m Model) check() tea.Cmd {
	return func() tea.Msg {
		r, err := m.be.Status(m.ctx)
		return statusMsg{r, err, time.Now()}
	}
}

// back returns to the dashboard and checks again.
func (m Model) back() (Model, tea.Cmd) {
	m.screen, m.plan = dashboard, nil
	if m.checking {
		return m, nil
	}
	m.checking = true
	return m, m.check()
}

// Update handles keys, window sizes and finished work.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.SetWidth(msg.Width)
		m.vp.SetHeight(max(1, msg.Height-4))
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case statusMsg:
		m.checking, m.err = false, msg.err
		if msg.r != nil {
			m.report, m.at = msg.r, msg.at
			m.sel = min(m.sel, max(0, len(m.report.Networks)-1))
		}
		m.tick++
		id := m.tick
		return m, tea.Tick(m.every, func(time.Time) tea.Msg { return tickMsg{id} })
	case tickMsg:
		// Off the dashboard, the next check waits for the way back.
		if msg.id != m.tick || m.checking || m.screen != dashboard {
			return m, nil
		}
		m.checking = true
		return m, m.check()
	case planMsg:
		if msg.err != nil {
			return m.finish([]string{"Couldn't plan " + m.network + ": " + msg.err.Error()}, true), nil
		}
		m.screen, m.plan = reviewing, msg.p
		var b bytes.Buffer
		engine.WritePlan(&b, msg.p.Plan, m.style)
		m.vp.SetContent(b.String())
		m.vp.GotoTop()
		return m, nil
	case applyMsg:
		log := msg.log
		if msg.err != nil {
			log = append(log, "", "Apply failed: "+msg.err.Error())
		} else {
			log = append(log, "", "Done.")
		}
		return m.finish(log, msg.err != nil), nil
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	if m.screen == reviewing || m.screen == done {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

// finish shows lines of output until a key is pressed.
func (m Model) finish(lines []string, failed bool) Model {
	m.screen, m.failed = done, failed
	m.vp.SetContent(strings.Join(lines, "\n"))
	m.vp.GotoTop()
	return m
}

func (m Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch m.screen {
	case applying:
		return m, nil // let it finish; the nodes undo changes they can't confirm
	case planning:
		if k == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	case dashboard:
		n := 0
		if m.report != nil {
			n = len(m.report.Networks)
		}
		switch k {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			m.sel = max(0, m.sel-1)
		case "down", "j":
			m.sel = min(max(0, n-1), m.sel+1)
		case "r":
			if !m.checking {
				m.checking = true
				return m, m.check()
			}
		case "p", "enter":
			if n > 0 {
				m.screen, m.network = planning, m.report.Networks[m.sel].Name
				be, ctx, name := m.be, m.ctx, m.network
				return m, func() tea.Msg {
					p, err := be.Plan(ctx, name)
					return planMsg{p, err}
				}
			}
		}
		return m, nil
	case reviewing:
		switch k {
		case "ctrl+c":
			return m, tea.Quit
		case "q", "esc":
			return m.back()
		case "a":
			if m.canApply() {
				m.screen = confirming
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case confirming:
		switch k {
		case "y":
			m.screen = applying
			p, ctx := m.plan, m.ctx
			return m, func() tea.Msg {
				var log []string
				err := p.Apply(ctx, func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) })
				return applyMsg{log, err}
			}
		case "n", "esc", "q":
			m.screen = reviewing
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	case done:
		if k == "ctrl+c" {
			return m, tea.Quit
		}
		switch k {
		case "up", "down", "k", "j", "pgup", "pgdown":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		return m.back()
	}
	return m, nil
}

func (m Model) canApply() bool {
	return m.plan != nil && len(m.plan.Problems) == 0 && !m.plan.Empty()
}

var (
	bold   = lipgloss.NewStyle().Bold(true)
	faint  = lipgloss.NewStyle().Faint(true)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	picked = lipgloss.NewStyle().Bold(true).Reverse(true)
)

// View draws the screen.
func (m Model) View() tea.View {
	var body, help string
	switch m.screen {
	case dashboard:
		body, help = m.dashboard(), "↑/↓ network · p plan · r check now · q quit"
	case planning:
		body, help = m.spin.View()+" Planning "+m.network+"…", "ctrl+c quit"
	case reviewing:
		body, help = m.vp.View(), "↑/↓ scroll · esc back"
		switch {
		case len(m.plan.Problems) > 0:
			help = "Fix the problems first. " + help
		case m.plan.Empty():
			help = "Nothing to change. " + help
		default:
			help = "a apply · " + help
		}
	case confirming:
		body, help = m.vp.View(), bold.Render("Apply these changes to "+m.network+"? y/n")
	case applying:
		body, help = m.spin.View()+" Applying "+m.network+"…", "Changes run to the end; nodes undo any they can't confirm."
	case done:
		body, help = m.vp.View(), "any key: back to the dashboard"
	}
	head := bold.Render("wglink") + "  " + faint.Render(m.title)
	switch {
	case m.checking:
		head += "  " + m.spin.View() + " checking"
	case !m.at.IsZero():
		head += "  " + faint.Render("checked "+m.at.Format("15:04:05"))
	}
	v := tea.NewView(head + "\n\n" + body + "\n\n" + faint.Render(help))
	v.AltScreen = true
	v.WindowTitle = "wglink"
	return v
}

func (m Model) dashboard() string {
	switch {
	case m.report == nil && m.err != nil:
		return red.Render("Couldn't check the networks: " + m.err.Error())
	case m.report == nil:
		return "Checking your networks…"
	case len(m.report.Networks) == 0:
		return "No networks yet. Connect two nodes with: wglink connect NODE NODE"
	}
	var list []string
	for i, n := range m.report.Networks {
		ok, all := 0, len(n.Links)
		worst := engine.LinkOK
		for _, l := range n.Links {
			switch l.State {
			case engine.LinkOK:
				ok++
			case engine.LinkDown, engine.LinkMissing:
				worst = engine.LinkDown
			default:
				if worst == engine.LinkOK {
					worst = l.State
				}
			}
		}
		mark := map[engine.LinkState]string{engine.LinkOK: green.Render("✓"), engine.LinkDown: red.Render("✗")}[worst]
		if mark == "" {
			mark = yellow.Render("•")
		}
		line := fmt.Sprintf("%s %s %d/%d", mark, n.Name, ok, all)
		if i == m.sel {
			line = picked.Render(" " + line + " ")
		} else {
			line = " " + line + " "
		}
		list = append(list, line)
	}
	left := bold.Render("NETWORKS") + "\n" + strings.Join(list, "\n")
	if m.err != nil {
		left += "\n\n" + red.Render("Last check failed:\n"+m.err.Error())
	}
	n := m.report.Networks[m.sel]
	var b bytes.Buffer
	engine.WriteStatus(&b, &engine.Report{Networks: []*engine.NetReport{n}, Errors: m.report.Errors}, m.style)
	right := strings.TrimRight(b.String(), "\n")
	gap := lipgloss.NewStyle().PaddingLeft(3)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, gap.Render(right))
}
