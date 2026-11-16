// Package nodetest fakes nodes for tests: wglink's real scripts run under a
// real shell, with stub WireGuard, systemd and iproute2 tools that keep their
// state in temp directories.
package nodetest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/runner"
)

// stubs stand in for system tools. They keep state in $STUB and log every
// call to $STUB/log. Private keys look like "priv000…1=" and their public
// keys like "pub0000…1=".
var stubs = map[string]string{
	"wg": `#!/bin/sh
echo "wg $*" >> "$STUB/log"
case $1 in
genkey) n=$(cat "$STUB/keyseq" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB/keyseq"; printf 'priv%039d=\n' "$((n + ${STUB_KEYBASE:-0}))" ;;
pubkey) sed 's/^priv/pub0/' ;;
syncconf) cp "$3" "$STUB/synced.$2" ;;
show)
	if [ "$2" = all ]; then cat "$STUB/listen" 2>/dev/null; exit 0; fi
	if [ "$2" = interfaces ]; then ls "$STUB/up" 2>/dev/null | tr '\n' ' '; echo; exit 0; fi
	if [ -e "$STUB/dump.$2" ]; then cat "$STUB/dump.$2"; exit 0; fi
	# Build a dump from the config; every peer was last seen at $STUB/handshake.
	hs=$(cat "$STUB/handshake" 2>/dev/null || echo 0)
	awk -v hs="$hs" '
	function flush() { if (pk != "") peers[n++] = pk "\t" psk "\t" ep "\t" aips "\t" hs "\t1000\t2000\t" ka }
	BEGIN { FS = " = " }
	/^PrivateKey/ { priv = $2 }
	/^ListenPort/ { port = $2 }
	/^\[Peer\]/ { flush(); pk = ""; psk = "(none)"; ep = "(none)"; aips = "(none)"; ka = "off"; next }
	/^PublicKey/ { pk = $2 }
	/^PresharedKey/ { psk = $2 }
	/^Endpoint/ { ep = $2 }
	/^AllowedIPs/ { aips = $2; gsub(/, /, ",", aips) }
	/^PersistentKeepalive/ { ka = $2 }
	END { flush(); pub = priv; sub(/^priv/, "pub0", pub); print priv "\t" pub "\t" port "\toff"; for (i = 0; i < n; i++) print peers[i] }
	' "$STUB_WGDIR/$2.conf" ;;
esac
`,
	"wg-quick": `#!/bin/sh
echo "wg-quick $*" >> "$STUB/log"
case $1 in
up) mkdir -p "$STUB/up"; touch "$STUB/up/$2" ;;
down) rm -f "$STUB/up/$2" ;;
strip) grep -v '^Address' "$STUB_WGDIR/$2.conf" ;;
esac
`,
	"systemctl": `#!/bin/sh
echo "systemctl $*" >> "$STUB/log"
unit=
for a; do unit=$a; done
iface=${unit#wg-quick@}
case $1 in
is-active) [ -e "$STUB/active/$unit" ] ;;
restart|start) mkdir -p "$STUB/up" "$STUB/active"; touch "$STUB/up/$iface" "$STUB/active/$unit"; [ ! -e "$STUB/fail" ] ;;
disable) rm -f "$STUB/up/$iface" "$STUB/active/$unit" ;;
stop) shift; for u; do rm -f "$STUB/timers/$u"; done ;;
esac
`,
	// Timers are files holding the command to run; FireRevert runs one.
	"systemd-run": `#!/bin/sh
echo "systemd-run $*" >> "$STUB/log"
unit=
while [ $# -gt 0 ]; do
	case $1 in
	--unit=*) unit=${1#--unit=} ;;
	--*) ;;
	*) break ;;
	esac
	shift
done
mkdir -p "$STUB/timers"
printf '%s\n' "$*" > "$STUB/timers/$unit.timer"
`,
	"ip": `#!/bin/sh
case "$*" in
"link show dev "*) [ -e "$STUB/up/$4" ] ;;
"link del dev "*) rm -f "$STUB/up/$4" ;;
"-o addr show") cat "$STUB/addr" 2>/dev/null ;;
"-o -4 route show table all") cat "$STUB/route4" 2>/dev/null ;;
"-o -6 route show table all") cat "$STUB/route6" 2>/dev/null ;;
esac
`,
	"ping": `#!/bin/sh
for ip; do :; done
grep -qx "$ip" "$STUB/pingable" 2>/dev/null || exit 1
echo "64 bytes from $ip: icmp_seq=1 ttl=64 time=0.42 ms"
`,
	"journalctl": "#!/bin/sh\necho 'journal: handshake failed'\n",
	// The clock reads $STUB/now when set.
	"date": "#!/bin/sh\nif [ \"$1\" = +%s ] && [ -e \"$STUB/now\" ]; then cat \"$STUB/now\"; exit 0; fi\nPATH=/usr/bin:/bin\nexec date \"$@\"\n",
	// Pretend to be root, as wglink requires.
	"id": "#!/bin/sh\nif [ \"$1\" = -u ]; then echo 0; else exec /usr/bin/id \"$@\"; fi\n",
}

// Shells lists the shells scripts must work under, by name.
func Shells(t testing.TB) map[string][]string {
	out := map[string][]string{}
	for name, argv := range map[string][]string{
		"dash":    {"dash"},
		"bash":    {"bash", "--posix"},
		"busybox": {"busybox", "sh"},
	} {
		if _, err := exec.LookPath(argv[0]); err == nil {
			out[name] = argv
		}
	}
	if len(out) == 0 {
		t.Skip("no POSIX shell found")
	}
	return out
}

// Shell returns one shell whose commands come from PATH, for tests that
// aren't about shell differences.
func Shell(t testing.TB) []string {
	for _, argv := range [][]string{{"dash"}, {"bash", "--posix"}} {
		if _, err := exec.LookPath(argv[0]); err == nil {
			return argv
		}
	}
	t.Skip("no dash or bash")
	return nil
}

// NeedStubbedIP skips tests that rely on the ip stub when the shell runs a
// built-in ip instead, as some busybox builds do.
func NeedStubbedIP(t testing.TB, shell []string) {
	t.Helper()
	bin := t.TempDir()
	stub := filepath.Join(bin, "ip")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell[0], append(shell[1:], "-c", "command -v ip")...)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != stub {
		t.Skipf("%s runs its own ip, so the stub can't stand in", shell[0])
	}
}

// Fake is one pretend node.
type Fake struct {
	t     testing.TB
	shell []string
	env   []string
	Stub  string // stub tool state
	Dir   string // stands in for /etc/wireguard
}

// New makes a fake node. keyBase offsets generated keys so that fakes in
// one test don't share keys.
func New(t testing.TB, shell []string, keyBase int) *Fake {
	t.Helper()
	f := &Fake{t: t, shell: shell, Stub: t.TempDir(), Dir: t.TempDir()}
	bin := t.TempDir()
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"STUB=" + f.Stub,
		"STUB_WGDIR=" + f.Dir,
		"STUB_KEYBASE=" + strconv.Itoa(keyBase),
	}
	return f
}

// Exec writes a wrapper that runs wglink's scripts as if on this fake and
// returns it as an exec prefix for wglink.yaml.
func (f *Fake) Exec() []string {
	f.t.Helper()
	shell, err := exec.LookPath(f.shell[0])
	if err != nil {
		f.t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, e := range f.env {
		k, v, _ := strings.Cut(e, "=")
		b.WriteString("export " + k + "=" + runner.Quote([]string{v}) + "\n")
	}
	b.WriteString(`if [ -e "$STUB/unreachable" ]; then cat "$STUB/unreachable" >&2; exit 255; fi` + "\n")
	// wglink runs "sh -s" with the script on stdin; aim it at this fake's directory.
	b.WriteString(`sed "s|^WG_DIR=.*|WG_DIR='$STUB_WGDIR'|" | exec ` + runner.Quote(append([]string{shell}, f.shell[1:]...)) + " -s\n")
	path := filepath.Join(f.Stub, "node.sh")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return []string{path}
}

// Runner runs scripts on the fake and fails the test if a private key ever
// appears in their output.
func (f *Fake) Runner() runner.Runner { return fakeRunner{f} }

// Fail makes every command on the fake fail, like an unreachable host.
func (f *Fake) Fail(msg string) { f.WriteStub("unreachable", msg) }

// Log returns every stub call so far.
func (f *Fake) Log() string {
	b, _ := os.ReadFile(filepath.Join(f.Stub, "log"))
	return string(b)
}

// WriteStub sets a stub's state file, such as "addr" or "dump.wg-lab".
func (f *Fake) WriteStub(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.Stub, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// SetUp marks an interface as up, and its systemd unit active if asked.
func (f *Fake) SetUp(iface string, active bool) {
	os.MkdirAll(filepath.Join(f.Stub, "up"), 0o755)
	os.MkdirAll(filepath.Join(f.Stub, "active"), 0o755)
	os.WriteFile(filepath.Join(f.Stub, "up", iface), nil, 0o644)
	if active {
		os.WriteFile(filepath.Join(f.Stub, "active", "wg-quick@"+iface), nil, 0o644)
	}
}

// FireRevert runs the timer a write armed to undo a change to iface, as
// systemd would once it expires.
func (f *Fake) FireRevert(iface string) {
	f.t.Helper()
	timer := filepath.Join(f.Stub, "timers", "wglink-revert-"+iface+".timer")
	cmd, err := os.ReadFile(timer)
	if err != nil {
		f.t.Fatalf("no revert timer for %s: %v", iface, err)
	}
	os.Remove(timer)
	argv := strings.Fields(string(cmd))
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = f.env
	if out, err := c.CombinedOutput(); err != nil {
		f.t.Fatalf("revert timer for %s: %v\n%s", iface, err, out)
	}
}

// Timer reports whether a revert timer for iface is armed.
func (f *Fake) Timer(iface string) bool {
	_, err := os.Stat(filepath.Join(f.Stub, "timers", "wglink-revert-"+iface+".timer"))
	return err == nil
}

// SetDown marks an interface as down.
func (f *Fake) SetDown(iface string) {
	os.Remove(filepath.Join(f.Stub, "up", iface))
	os.Remove(filepath.Join(f.Stub, "active", "wg-quick@"+iface))
}

type fakeRunner struct{ f *Fake }

func (r fakeRunner) String() string { return "fake" }

func (r fakeRunner) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	f := r.f
	if msg, err := os.ReadFile(filepath.Join(f.Stub, "unreachable")); err == nil {
		return nil, &runner.ExitError{Target: "fake", Code: 255, Stderr: string(msg)}
	}
	cmd := exec.CommandContext(ctx, f.shell[0], append(f.shell[1:], argv[1:]...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = f.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if bytes.Contains(stdout.Bytes(), []byte("priv0")) || bytes.Contains(stderr.Bytes(), []byte("priv0")) {
		f.t.Errorf("a private key leaked into script output:\n%s\n%s", stdout.String(), stderr.String())
	}
	if err != nil {
		return stdout.Bytes(), &runner.ExitError{Target: "fake", Code: cmd.ProcessState.ExitCode(), Stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}
