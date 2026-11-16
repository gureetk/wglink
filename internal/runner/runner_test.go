package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// awkward holds strings that break naive quoting.
var awkward = []string{
	"plain", "two words", "it's", `"double"`, "$HOME", "`id`", `back\slash`,
	"semi;colon", "pipe|line", "new\nline", "tab\there", "*", "~", "", "-n", "ünïcode", "a'b'c",
}

func TestQuoteRoundTripsThroughSh(t *testing.T) {
	for _, shell := range []string{"sh", "bash", "dash", "busybox"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		args := []string{path, "-c", "eval \"set -- $1\"; for a; do printf '%s\\0' \"$a\"; done", "x", Quote(awkward)}
		if shell == "busybox" {
			args = append([]string{path, "sh"}, args[1:]...)
		}
		out, err := exec.Command(args[0], args[1:]...).Output()
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if len(got) != len(awkward) {
			t.Fatalf("%s: got %d args %q, want %d", shell, len(got), got, len(awkward))
		}
		for i := range awkward {
			if got[i] != awkward[i] {
				t.Errorf("%s: arg %d = %q, want %q", shell, i, got[i], awkward[i])
			}
		}
	}
}

func TestQuoteLeavesPlainWords(t *testing.T) {
	if got := Quote([]string{"pct", "exec", "101", "--", "sh", "-s"}); got != "pct exec 101 -- sh -s" {
		t.Errorf("got %q", got)
	}
}

func TestLocalRun(t *testing.T) {
	ctx := context.Background()
	out, err := Local{}.Run(ctx, []string{"cat"}, []byte("hello"))
	if err != nil || string(out) != "hello" {
		t.Errorf("cat = %q, %v", out, err)
	}
	_, err = Local{}.Run(ctx, []string{"sh", "-c", "echo oops >&2; exit 3"}, nil)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || !strings.Contains(err.Error(), "local: oops") {
		t.Errorf("err = %v", err)
	}
	if _, err := (Local{}).Run(ctx, []string{"/no/such/binary"}, nil); err == nil {
		t.Error("missing binary succeeded")
	}
	if _, err := (Local{}).Run(ctx, nil, nil); err == nil {
		t.Error("empty command succeeded")
	}
}

func TestRunTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Local{}.Run(ctx, []string{"sleep", "5"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
}

func TestExecAndVia(t *testing.T) {
	ctx := context.Background()
	e := Exec{Prefix: []string{"env", "WGLINK_TEST=1"}}
	out, err := e.Run(ctx, []string{"sh", "-c", "echo $WGLINK_TEST"}, nil)
	if err != nil || string(out) != "1\n" {
		t.Errorf("exec = %q, %v", out, err)
	}
	v := Via{Host: e, Prefix: []string{"sh", "-c", `echo "$0 $1"; exit 4`}, Name: "guest 7"}
	out, err = v.Run(ctx, []string{"a", "b"}, nil)
	var ee *ExitError
	if string(out) != "a b\n" || !errors.As(err, &ee) || ee.Target != "guest 7" || ee.Code != 4 {
		t.Errorf("via = %q, %v", out, err)
	}
	p := PCT(Local{}, 101)
	if strings.Join(p.Prefix, " ") != "pct exec 101 --" || p.String() != "pct 101 on local" {
		t.Errorf("PCT = %+v", p)
	}
}

// fakeSSH puts an ssh on PATH that logs its args and runs the remote
// command with sh, the way sshd hands it to a login shell.
func fakeSSH(t *testing.T) (logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + logFile + `"
if [ "$1" = -G ]; then echo "user root"; echo "hostname 192.168.1.10"; exit 0; fi
for last; do :; done
exec sh -c "$last"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A pass-through sudo, so tests don't depend on the real one.
	sudo := "#!/bin/sh\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(sudo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func TestSSHQuotesForTheRemoteShell(t *testing.T) {
	fakeSSH(t)
	s := SSH{Dest: "root@pve1"}
	argv := append([]string{"sh", "-c", "for a; do printf '%s\\0' \"$a\"; done", "sh"}, awkward...)
	out, err := s.Run(context.Background(), argv, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if strings.Join(got, "|") != strings.Join(awkward, "|") {
		t.Errorf("remote saw %q, want %q", got, awkward)
	}
}

func TestSSHArgs(t *testing.T) {
	log := fakeSSH(t)
	s := SSH{Dest: "admin@nas.lan", Sudo: true, Config: "/tmp/cfg", MuxDir: "/run/x"}
	if _, err := s.Run(context.Background(), []string{"true"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	args := string(b)
	for _, want := range []string{"BatchMode=yes", "-F\n/tmp/cfg\n", "ControlPath=/run/x/%C", "ControlMaster=auto", "--\nadmin@nas.lan\nsudo -n -- true\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh args lack %q:\n%s", want, args)
		}
	}
	if _, err := s.Fresh().Run(context.Background(), []string{"true"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "ControlPath=none") {
		t.Errorf("Fresh still multiplexes:\n%s", b)
	}
}

func TestSSHHost(t *testing.T) {
	fakeSSH(t)
	host, err := SSH{Dest: "pve1"}.Host(context.Background())
	if err != nil || host != "192.168.1.10" {
		t.Errorf("Host = %q, %v", host, err)
	}
}

func TestSSHHints(t *testing.T) {
	fakeSSH(t)
	_, err := SSH{Dest: "x"}.Run(context.Background(), []string{"sh", "-c", "echo 'root@x: Permission denied (publickey).' >&2; exit 255"}, nil)
	if err == nil || !strings.Contains(err.Error(), "hint: set up key-based login") {
		t.Errorf("err = %v", err)
	}
}

func TestMuxDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	if err := os.MkdirAll(filepath.Join(base, "wglink-ssh"), 0o777); err != nil {
		t.Fatal(err)
	}
	dir, err := MuxDir()
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
	}
}
