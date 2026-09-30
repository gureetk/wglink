package cli

import (
	"bytes"
	"io"
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

func TestVersionRejectsArgs(t *testing.T) {
	if _, err := runCLI("version", "extra"); err == nil {
		t.Error("version with an extra argument succeeded, want an error")
	}
}

// runCLI runs a fresh root with args and returns what it wrote to stdout.
func runCLI(args ...string) (string, error) {
	var out bytes.Buffer
	root := newRoot("v1.2.3")
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}
