package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// QM reaches a Proxmox VM through the QEMU guest agent, using qm on its
// host. The agent runs commands as root inside the guest.
type QM struct {
	Host    Runner
	VMID    int
	Timeout time.Duration // how long the agent waits for a command; 2 minutes if 0
}

// maxAgentInput is the most stdin qm guest exec passes to the guest.
const maxAgentInput = 1 << 20

func (q QM) String() string { return fmt.Sprintf("qm %d on %s", q.VMID, q.Host) }

// Run implements Runner.
func (q QM) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	if len(stdin) > maxAgentInput {
		return nil, fmt.Errorf("%s: %d bytes of input is over the guest agent's 1 MiB limit", q, len(stdin))
	}
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	cmd := []string{"qm", "guest", "exec", strconv.Itoa(q.VMID), "--timeout", strconv.Itoa(int(timeout.Seconds()))}
	if len(stdin) > 0 {
		cmd = append(cmd, "--pass-stdin", "1")
	}
	cmd = append(append(cmd, "--"), argv...)
	out, err := q.Host.Run(ctx, cmd, stdin)
	if err != nil {
		var ee *ExitError
		if errors.As(err, &ee) {
			return nil, &ExitError{Target: q.String(), Code: ee.Code, Stderr: agentHint(ee.Stderr)}
		}
		return nil, err
	}
	var res struct {
		Exited       flag   `json:"exited"`
		ExitCode     *int   `json:"exitcode"`
		Signal       *int   `json:"signal"`
		OutData      string `json:"out-data"`
		ErrData      string `json:"err-data"`
		OutTruncated flag   `json:"out-truncated"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("%s: can't read qm's answer %q: %w", q, truncate(string(out), 200), err)
	}
	switch {
	case !bool(res.Exited):
		return nil, fmt.Errorf("%s: still running after %s, so the guest agent stopped waiting", q, timeout)
	case bool(res.OutTruncated):
		return nil, fmt.Errorf("%s: the guest agent cut the output short", q)
	case res.Signal != nil:
		return []byte(res.OutData), &ExitError{Target: q.String(), Code: 128 + *res.Signal, Stderr: res.ErrData}
	case res.ExitCode != nil && *res.ExitCode != 0:
		return []byte(res.OutData), &ExitError{Target: q.String(), Code: *res.ExitCode, Stderr: res.ErrData}
	}
	return []byte(res.OutData), nil
}

// flag reads the booleans qm prints, which may be 0/1 or false/true.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "1", "true", `"1"`:
		*f = true
	default:
		*f = false
	}
	return nil
}

// agentHint adds advice for the ways the guest agent is commonly missing.
func agentHint(stderr string) string {
	hints := []struct{ match, hint string }{
		{"No QEMU guest agent configured", "enable the QEMU guest agent in the VM's options, then restart the VM"},
		{"QEMU guest agent is not running", "install and start qemu-guest-agent inside the VM"},
		{"not running", "start the VM"},
		{"does not exist", "check the VM ID, and that the VM lives on this host"},
	}
	for _, h := range hints {
		if strings.Contains(stderr, h.match) {
			return strings.TrimSpace(stderr) + "\nhint: " + h.hint
		}
	}
	return stderr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
