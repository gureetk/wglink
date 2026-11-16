package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// agentHost stands in for a Proxmox host. Its qm runs the command locally,
// as the guest agent would inside the VM, and answers in qm's JSON; or it
// gives a canned reply or error.
type agentHost struct {
	argv  []string
	stdin []byte
	reply string
	err   error
}

func (h *agentHost) String() string { return "pve1" }

func (h *agentHost) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	h.argv, h.stdin = argv, stdin
	if h.err != nil || h.reply != "" {
		return []byte(h.reply), h.err
	}
	i := slices.Index(argv, "--")
	cmd := exec.CommandContext(ctx, argv[i+1], argv[i+2:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	var xe *exec.ExitError
	if err := cmd.Run(); errors.As(err, &xe) {
		code = xe.ExitCode()
	}
	return json.Marshal(map[string]any{"exited": 1, "exitcode": code, "out-data": out.String(), "err-data": errOut.String()})
}

func TestQMRunsThroughTheAgent(t *testing.T) {
	ctx := context.Background()
	h := &agentHost{}
	q := QM{Host: h, VMID: 105}
	out, err := q.Run(ctx, []string{"sh", "-s"}, []byte("echo hi; echo oops >&2; exit 3"))
	var ee *ExitError
	if string(out) != "hi\n" || !errors.As(err, &ee) || ee.Code != 3 || ee.Stderr != "oops\n" || ee.Target != "qm 105 on pve1" {
		t.Errorf("run = %q, %#v", out, err)
	}
	if got := strings.Join(h.argv, " "); got != "qm guest exec 105 --timeout 120 --pass-stdin 1 -- sh -s" {
		t.Errorf("argv = %s", got)
	}
	q.Timeout = 30 * time.Second
	if out, err := q.Run(ctx, []string{"echo", "ok"}, nil); err != nil || string(out) != "ok\n" {
		t.Errorf("run = %q, %v", out, err)
	}
	if got := strings.Join(h.argv, " "); got != "qm guest exec 105 --timeout 30 -- echo ok" {
		t.Errorf("argv without stdin = %s", got)
	}
}

func TestQMReplies(t *testing.T) {
	for _, c := range []struct{ reply, want string }{
		{`{"exited": 0, "pid": 4242}`, "still running after 2m0s"},
		{`{"exited": true, "exitcode": 0, "out-data": "x", "out-truncated": true}`, "cut the output short"},
		{`{"exited": 1, "signal": 9}`, "qm 105 on pve1: exit status 137"},
		{`VM 105 qmp command failed`, "can't read qm's answer"},
	} {
		_, err := QM{Host: &agentHost{reply: c.reply}, VMID: 105}.Run(context.Background(), []string{"true"}, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("reply %s: error %v, want %q", c.reply, err, c.want)
		}
	}
}

func TestQMHostErrors(t *testing.T) {
	h := &agentHost{err: &ExitError{Target: "ssh root@pve1", Code: 255, Stderr: "QEMU guest agent is not running\n"}}
	_, err := QM{Host: h, VMID: 105}.Run(context.Background(), []string{"true"}, nil)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Target != "qm 105 on pve1" || !strings.Contains(ee.Stderr, "hint: install and start qemu-guest-agent") {
		t.Errorf("err = %#v", err)
	}

	big := bytes.Repeat([]byte("x"), maxAgentInput+1)
	h = &agentHost{}
	if _, err := (QM{Host: h, VMID: 105}).Run(context.Background(), []string{"sh", "-s"}, big); err == nil || h.argv != nil {
		t.Errorf("oversized input: %v, ran %v", err, h.argv)
	}
}
