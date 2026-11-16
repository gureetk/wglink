package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/runner"
	"github.com/gureetk/wglink/internal/state"
)

// lostRunner is an SSH connection that can't get through any more.
type lostRunner struct{}

func (lostRunner) String() string { return "ssh pve1" }

func (lostRunner) Run(context.Context, []string, []byte) ([]byte, error) {
	return nil, &runner.ExitError{Target: "ssh pve1", Code: 255, Stderr: "ssh: connect to host pve1 port 22: Connection timed out"}
}

// sshLike treats the named nodes as reached over SSH, with new connections
// given by conn.
func (l *lab) sshLike(conn func(name string) runner.Runner, names ...string) {
	l.fresh = func(name string) (runner.Runner, bool) {
		for _, n := range names {
			if n == name {
				return conn(name), true
			}
		}
		return nil, false
	}
}

func TestRevertConfirmedOverANewConnection(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.sshLike(func(name string) runner.Runner { return l.fakes[name].Runner() }, "pve1")
	l.apply(labNet, Options{})
	if log := l.fakes["pve1"].Log(); !strings.Contains(log, "systemd-run") || l.fakes["pve1"].Timer("wg-lab") {
		t.Errorf("pve1 should arm a timer and confirm it:\n%s", log)
	}
	if strings.Contains(l.fakes["nas"].Log(), "systemd-run") {
		t.Error("nas isn't an SSH node but armed a timer")
	}
}

func TestRevertWhenContactIsLost(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(labNet, Options{})
	before := l.conf("pve1", "wg-lab")
	st, _ := state.Load(l.dir)
	applied := st.Networks["lab"].Members["pve1"].Applied

	l.sshLike(func(string) runner.Runner { return lostRunner{} }, "pve1")
	changed := base + "  lab: {members: [pve1, nas], keepalive: 25}\n"
	s, p := l.plan(changed, Options{})
	err := s.Apply(ctx, p, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "couldn't confirm the change to wg-lab on pve1, so pve1 puts the previous config back within 1m0s") || !strings.Contains(err.Error(), "Connection timed out") {
		t.Fatalf("apply = %v", err)
	}
	if !l.fakes["pve1"].Timer("wg-lab") || !strings.Contains(l.conf("pve1", "wg-lab"), "PersistentKeepalive = 25") {
		t.Fatal("the change should be in place with its timer armed")
	}
	st, _ = state.Load(l.dir)
	if st.Networks["lab"].Members["pve1"].Applied != applied {
		t.Error("state records a change that wasn't confirmed")
	}

	l.fakes["pve1"].FireRevert("wg-lab")
	if l.conf("pve1", "wg-lab") != before {
		t.Errorf("after the timer:\n%s\nwant:\n%s", l.conf("pve1", "wg-lab"), before)
	}
	if _, p := l.plan(changed, Options{}); members(p, "lab")["pve1"].Action != Live {
		t.Errorf("plan after the revert:\n%s", render(p))
	}
}

func TestNoRevert(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.sshLike(func(string) runner.Runner { return lostRunner{} }, "pve1", "pi")
	l.apply(labNet, Options{NoRevert: true})
	if strings.Contains(l.fakes["pve1"].Log(), "systemd-run") {
		t.Error("--no-revert armed a timer")
	}

	// pi has no systemd, so plan says it can't protect it.
	_, p := l.plan(base+"  lab: {members: [pve1, nas, pi]}\n", Options{})
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "pi has no systemd, so if a change cuts wglink off from it, it can't undo the change by itself") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	if _, p := l.plan(base+"  lab: {members: [pve1, nas, pi]}\n", Options{NoRevert: true}); strings.Contains(strings.Join(p.Warnings, "\n"), "pi has no systemd") {
		t.Errorf("warned despite --no-revert: %v", p.Warnings)
	}
}
