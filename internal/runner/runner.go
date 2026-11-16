// Package runner runs commands on nodes: locally, over SSH, through an exec
// prefix such as docker exec, or inside a Proxmox container via its host.
//
// Commands are argv lists and never pass through a local shell. Over SSH the
// remote side does use a shell, so every argument is quoted.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner runs commands on one node.
type Runner interface {
	// Run runs argv with stdin and returns its stdout.
	Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error)
	// String names the target in messages, e.g. "ssh root@pve1".
	String() string
}

// ExitError reports a command that ran and failed.
type ExitError struct {
	Target string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("%s: exit status %d", e.Target, e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Target, msg)
}

// Local runs commands on this machine.
type Local struct {
	Sudo bool // use sudo -n when not root
}

func (l Local) String() string { return "local" }

// Run implements Runner.
func (l Local) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	if l.Sudo && os.Geteuid() != 0 {
		argv = append([]string{"sudo", "-n", "--"}, argv...)
	}
	return run(ctx, l.String(), argv, stdin)
}

// Exec runs commands through a fixed local prefix, e.g. docker exec -i web.
type Exec struct {
	Prefix []string
}

func (e Exec) String() string { return "exec " + strings.Join(e.Prefix, " ") }

// Run implements Runner.
func (e Exec) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	return run(ctx, e.String(), append(append([]string(nil), e.Prefix...), argv...), stdin)
}

// Via runs commands on a host with a prefix, e.g. pct exec 101 -- on a Proxmox host.
type Via struct {
	Host   Runner
	Prefix []string
	Name   string
}

func (v Via) String() string { return v.Name }

// Run implements Runner.
func (v Via) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	out, err := v.Host.Run(ctx, append(append([]string(nil), v.Prefix...), argv...), stdin)
	var ee *ExitError
	if errors.As(err, &ee) {
		return out, &ExitError{Target: v.Name, Code: ee.Code, Stderr: ee.Stderr}
	}
	return out, err
}

// PCT reaches a Proxmox container through its host.
func PCT(host Runner, vmid int) Via {
	id := fmt.Sprint(vmid)
	return Via{Host: host, Prefix: []string{"pct", "exec", id, "--"}, Name: fmt.Sprintf("pct %s on %s", id, host)}
}

// run executes argv locally and wraps failures in ExitError.
func run(ctx context.Context, target string, argv []string, stdin []byte) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w", target, ctx.Err())
	}
	var xe *exec.ExitError
	if errors.As(err, &xe) {
		return stdout.Bytes(), &ExitError{Target: target, Code: xe.ExitCode(), Stderr: stderr.String()}
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w", target, err)
	}
	return stdout.Bytes(), nil
}

// Quote quotes argv for a POSIX shell. Plain words stay unquoted, which also
// keeps them safe under shells like fish.
func Quote(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = quoteWord(a)
	}
	return strings.Join(q, " ")
}

func quoteWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
