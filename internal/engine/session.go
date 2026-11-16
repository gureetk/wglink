// Package engine plans, applies and reports on wglink networks: it probes
// nodes, resolves addresses, renders configs and changes only what differs.
package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/runner"
	"github.com/gureetk/wglink/internal/state"
)

// Session is an inventory, its state and the way to reach each node.
type Session struct {
	Dir   string
	Inv   *config.Inventory
	State *state.State

	// Timeout bounds each command on a node.
	Timeout time.Duration
	// Parallel bounds how many nodes are contacted at once.
	Parallel int

	// Hooks for tests.
	newRunner func(n *config.Node) (runner.Runner, error)
	wgDir     func(node string) string

	mu        sync.Mutex
	runners   map[string]runner.Runner
	muxDir    string
	endpoints map[string]config.Endpoint
}

// Open loads wglink.yaml and wglink.state.json from dir.
func Open(dir string) (*Session, error) {
	inv, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		return nil, err
	}
	st, err := state.Load(dir)
	if err != nil {
		return nil, err
	}
	return New(dir, inv, st), nil
}

// New returns a session over an already loaded inventory and state.
func New(dir string, inv *config.Inventory, st *state.State) *Session {
	return &Session{
		Dir: dir, Inv: inv, State: st,
		Timeout:   2 * time.Minute,
		Parallel:  8,
		runners:   map[string]runner.Runner{},
		endpoints: map[string]config.Endpoint{},
	}
}

// Runner returns the runner for a node, building it on first use.
func (s *Session) Runner(name string) (runner.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runnerLocked(name)
}

func (s *Session) runnerLocked(name string) (runner.Runner, error) {
	if r, ok := s.runners[name]; ok {
		return r, nil
	}
	n, ok := s.Inv.Nodes[name]
	if !ok {
		return nil, fmt.Errorf("no node named %s", name)
	}
	var r runner.Runner
	var err error
	if s.newRunner != nil {
		r, err = s.newRunner(n)
	} else {
		r, err = s.build(n)
	}
	if err != nil {
		return nil, err
	}
	s.runners[name] = r
	return r, nil
}

func (s *Session) build(n *config.Node) (runner.Runner, error) {
	switch n.Reach() {
	case config.ReachSSH:
		if s.muxDir == "" {
			dir, err := runner.MuxDir()
			if err != nil {
				return nil, fmt.Errorf("ssh control directory: %w", err)
			}
			s.muxDir = dir
		}
		return runner.SSH{Dest: n.SSH, Sudo: n.Sudo, Config: os.Getenv("WGLINK_SSH_CONFIG"), MuxDir: s.muxDir}, nil
	case config.ReachLocal:
		return runner.Local{Sudo: n.Sudo}, nil
	case config.ReachExec:
		return runner.Exec{Prefix: n.Exec}, nil
	case config.ReachPCT:
		host, err := s.runnerLocked(n.PCT.Via)
		if err != nil {
			return nil, err
		}
		return runner.PCT(host, n.PCT.VMID), nil
	}
	return nil, fmt.Errorf("node %s has no reach method", n.Name)
}

// Node returns the script runner for a node.
func (s *Session) Node(name string) (node.Node, error) {
	r, err := s.Runner(name)
	if err != nil {
		return node.Node{}, err
	}
	dir := ""
	if s.wgDir != nil {
		dir = s.wgDir(name)
	}
	return node.Node{Runner: r, Dir: dir, Service: s.Inv.Nodes[name].Service}, nil
}

// Endpoint returns where peers dial a node: its endpoint setting, or for
// ssh nodes the host ssh connects to.
func (s *Session) Endpoint(ctx context.Context, name string) (config.Endpoint, error) {
	n := s.Inv.Nodes[name]
	if n.Endpoint != nil {
		return *n.Endpoint, nil
	}
	s.mu.Lock()
	e, ok := s.endpoints[name]
	s.mu.Unlock()
	if ok {
		return e, nil
	}
	r, err := s.Runner(name)
	if err != nil {
		return config.Endpoint{}, err
	}
	sshRunner, ok := r.(runner.SSH)
	if !ok {
		return config.Endpoint{}, fmt.Errorf("node %s: set endpoint (or \"none\")", name)
	}
	host, err := sshRunner.Host(ctx)
	if err != nil {
		return config.Endpoint{}, err
	}
	e, err = config.ParseEndpoint(host)
	if err != nil {
		return config.Endpoint{}, fmt.Errorf("node %s: ssh resolves to %q; set endpoint instead", name, host)
	}
	s.mu.Lock()
	s.endpoints[name] = e
	s.mu.Unlock()
	return e, nil
}

// SaveState writes the state file.
func (s *Session) SaveState() error { return s.State.Save(s.Dir) }

// each runs fn for every name, at most Parallel at a time, and returns
// errors by name.
func (s *Session) each(ctx context.Context, names []string, fn func(ctx context.Context, name string) error) map[string]error {
	sort.Strings(names)
	errs := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, max(1, s.Parallel))
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			ctx, cancel := context.WithTimeout(ctx, s.Timeout)
			defer cancel()
			if err := fn(ctx, name); err != nil {
				mu.Lock()
				errs[name] = err
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errs
}
