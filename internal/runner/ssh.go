package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SSH runs commands over OpenSSH, so ~/.ssh/config, agents, jump hosts and
// known_hosts all work as usual. It never prompts and never skips host key checks.
type SSH struct {
	Dest   string // user@host, ssh://user@host:port or a ~/.ssh/config alias
	Sudo   bool   // run as root through sudo -n
	Config string // passed as ssh -F when set
	MuxDir string // directory for shared connections; empty opens one per command
}

func (s SSH) String() string { return "ssh " + s.Dest }

// Run implements Runner.
func (s SSH) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	if s.Sudo {
		argv = append([]string{"sudo", "-n", "--"}, argv...)
	}
	cmd := append(append([]string{"ssh"}, s.args()...), "--", s.Dest, Quote(argv))
	out, err := run(ctx, s.String(), cmd, stdin)
	var ee *ExitError
	if errors.As(err, &ee) {
		ee.Stderr = withHint(ee.Stderr)
	}
	return out, err
}

// Fresh returns a copy that opens a new connection rather than reusing one,
// to prove a host is still reachable after a network change.
func (s SSH) Fresh() SSH {
	s.MuxDir = ""
	return s
}

// Host returns the hostname ssh would connect to, after ~/.ssh/config.
func (s SSH) Host(ctx context.Context) (string, error) {
	args := []string{"ssh", "-G"}
	if s.Config != "" {
		args = append(args, "-F", s.Config)
	}
	out, err := run(ctx, s.String(), append(args, "--", s.Dest), nil)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if host, ok := strings.CutPrefix(sc.Text(), "hostname "); ok {
			return host, nil
		}
	}
	return "", fmt.Errorf("%s: ssh -G printed no hostname", s)
}

func (s SSH) args() []string {
	a := []string{"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	if s.Config != "" {
		a = append(a, "-F", s.Config)
	}
	if s.MuxDir == "" {
		return append(a, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	}
	return append(a,
		"-o", "ControlMaster=auto",
		"-o", "ControlPath="+filepath.Join(s.MuxDir, "%C"),
		"-o", "ControlPersist=60",
	)
}

// MuxDir returns a private directory for SSH control sockets.
func MuxDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		var err error
		if base, err = os.UserCacheDir(); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(base, "wglink-ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Sockets grant access to open sessions, so keep the directory private.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// withHint adds advice for the failures people hit most.
func withHint(stderr string) string {
	hints := []struct{ match, hint string }{
		{"Permission denied (publickey", "set up key-based login (e.g. ssh-copy-id); wglink can't type passwords"},
		{"Host key verification failed", "connect once with plain ssh to check and accept the host key"},
		{"a password is required", "allow this user passwordless sudo, or connect as root"},
		{"a terminal is required", "allow this user passwordless sudo, or connect as root"},
		{"Could not resolve hostname", "check the address, or add a Host entry to ~/.ssh/config"},
	}
	for _, h := range hints {
		if strings.Contains(stderr, h.match) {
			return strings.TrimSpace(stderr) + "\nhint: " + h.hint
		}
	}
	return stderr
}
