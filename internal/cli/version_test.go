package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		t.Run(arg, func(t *testing.T) {
			got, err := runCLI(arg)
			if err != nil {
				t.Fatal(err)
			}
			if want := "wglink version v1.2.3\n"; got != want {
				t.Errorf("output = %q, want %q", got, want)
			}
		})
	}
}

func TestNoArgsPrintsHelp(t *testing.T) {
	got, err := runCLI()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Usage:") {
		t.Errorf("output = %q, want help text", got)
	}
}

func TestRejectsBadArgs(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"version", "extra"},
		{"--no-such-flag"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := runCLI(args...); err == nil {
				t.Error("succeeded, want an error")
			}
		})
	}
}

// runCLI runs a fresh root with args and returns what it wrote to stdout.
func runCLI(args ...string) (string, error) { return runCLIIn("", args...) }

// runCLIIn is runCLI with text on stdin.
func runCLIIn(stdin string, args ...string) (string, error) {
	var out bytes.Buffer
	root := newRoot("v1.2.3")
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetIn(strings.NewReader(stdin))
	// A nil slice makes cobra fall back to os.Args.
	root.SetArgs(append([]string{}, args...))
	err := root.Execute()
	return out.String(), err
}
