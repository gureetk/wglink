package node

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/runner"
	"github.com/gureetk/wglink/internal/wgconf"
)

// stubs stand in for wg, wg-quick, systemctl, ip, ping and journalctl.
// They keep state in $STUB and log every call to $STUB/log.
var stubs = map[string]string{
	"wg": `#!/bin/sh
echo "wg $*" >> "$STUB/log"
case $1 in
genkey) n=$(cat "$STUB/keyseq" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB/keyseq"; printf 'priv%039d=\n' "$n" ;;
pubkey) sed 's/^priv/pub0/' ;;
syncconf) cp "$3" "$STUB/synced.$2" ;;
show)
	if [ "$2" = all ]; then cat "$STUB/listen" 2>/dev/null; exit 0; fi
	cat "$STUB/dump.$2" ;;
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
esac
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
}

// shells lists the shells the scripts must work under.
func shells(t *testing.T) map[string][]string {
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

// needStubbedIP skips tests that rely on the ip stub when the shell runs a
// built-in ip instead, as some busybox builds do.
func needStubbedIP(t *testing.T, shell []string) {
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

// shRunner runs scripts locally under a chosen shell with the stubs on PATH,
// and fails the test if a private key ever appears in the output.
type shRunner struct {
	t     *testing.T
	shell []string
	env   []string
}

func (r shRunner) String() string { return "test" }

func (r shRunner) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.shell[0], append(r.shell[1:], argv[1:]...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if bytes.Contains(stdout.Bytes(), []byte("priv0")) || bytes.Contains(stderr.Bytes(), []byte("priv0")) {
		r.t.Errorf("a private key leaked into script output:\n%s\n%s", stdout.String(), stderr.String())
	}
	if err != nil {
		return stdout.Bytes(), &runner.ExitError{Target: "test", Code: cmd.ProcessState.ExitCode(), Stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

type env struct {
	n    Node
	stub string
	dir  string
}

func setup(t *testing.T, shell []string, service string) *env {
	t.Helper()
	stub, dir, bin := t.TempDir(), t.TempDir(), t.TempDir()
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := shRunner{t: t, shell: shell, env: []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"STUB=" + stub,
		"STUB_WGDIR=" + dir,
	}}
	return &env{n: Node{Runner: r, Dir: dir, Service: service}, stub: stub, dir: dir}
}

func (e *env) log() string {
	b, _ := os.ReadFile(filepath.Join(e.stub, "log"))
	return string(b)
}

func (e *env) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.stub, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) up(t *testing.T, iface string, active bool) {
	t.Helper()
	os.MkdirAll(filepath.Join(e.stub, "up"), 0o755)
	os.MkdirAll(filepath.Join(e.stub, "active"), 0o755)
	os.WriteFile(filepath.Join(e.stub, "up", iface), nil, 0o644)
	if active {
		os.WriteFile(filepath.Join(e.stub, "active", "wg-quick@"+iface), nil, 0o644)
	}
}

var ctx = context.Background()

const pubOf1 = "pub0000000000000000000000000000000000000001="

func rendered(peerPSK bool) []byte {
	return wgconf.Render(wgconf.Interface{
		Network:    "lab",
		Addresses:  []netip.Prefix{netip.MustParsePrefix("10.99.0.1/24")},
		ListenPort: 51820,
		Peers: []wgconf.Peer{{
			Name: "nas", PublicKey: pubOf1, PresharedKey: peerPSK,
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.99.0.2/32")}, Endpoint: "nas.lan:51820",
		}},
	})
}

func TestScripts(t *testing.T) {
	for name, shell := range shells(t) {
		t.Run(name, func(t *testing.T) {
			t.Run("key", func(t *testing.T) { testKey(t, shell) })
			t.Run("write", func(t *testing.T) { testWrite(t, shell) })
			t.Run("write modes", func(t *testing.T) { testWriteModes(t, shell) })
			t.Run("psk", func(t *testing.T) { testPSK(t, shell) })
			t.Run("remove", func(t *testing.T) { testRemove(t, shell) })
			t.Run("probe", func(t *testing.T) { testProbe(t, shell) })
			t.Run("status", func(t *testing.T) { testStatus(t, shell) })
		})
	}
}

func testKey(t *testing.T, shell []string) {
	e := setup(t, shell, "none")
	pub, created, err := e.n.EnsureKey(ctx, "wg-lab")
	if err != nil || !created || pub != pubOf1 {
		t.Fatalf("EnsureKey = %q, %v, %v", pub, created, err)
	}
	fi, err := os.Stat(filepath.Join(e.dir, "wg-lab.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key file: %v, %v", fi, err)
	}
	pub2, created, err := e.n.EnsureKey(ctx, "wg-lab")
	if err != nil || created || pub2 != pub {
		t.Errorf("second EnsureKey = %q, %v, %v; want the same key", pub2, created, err)
	}
	if _, _, err := e.n.EnsureKey(ctx, "bad name;rm"); err == nil {
		t.Error("EnsureKey accepted a bad interface name")
	}
}

func testWrite(t *testing.T, shell []string) {
	e := setup(t, shell, "systemd")
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err == nil || !strings.Contains(err.Error(), "no private key") {
		t.Fatalf("Write without a key: %v", err)
	}
	if _, _, err := e.n.EnsureKey(ctx, "wg-lab"); err != nil {
		t.Fatal(err)
	}
	pub, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Restart})
	if err != nil {
		t.Fatal(err)
	}
	if wgconf.Hash(pub) != wgconf.Hash(rendered(false)) {
		t.Errorf("public form on node differs from what was sent:\n%s", pub)
	}
	conf, _ := os.ReadFile(filepath.Join(e.dir, "wg-lab.conf"))
	if !strings.Contains(string(conf), "[Interface]\nPrivateKey = priv") {
		t.Errorf("private key not inserted after [Interface]:\n%s", conf)
	}
	if fi, _ := os.Stat(filepath.Join(e.dir, "wg-lab.conf")); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", fi.Mode().Perm())
	}
	if log := e.log(); !strings.Contains(log, "systemctl enable --quiet wg-quick@wg-lab") || !strings.Contains(log, "systemctl restart wg-quick@wg-lab") {
		t.Errorf("unit not enabled and started:\n%s", log)
	}
	// A second write keeps the old file as .prev.
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Live}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "wg-lab.conf.prev")); err != nil {
		t.Errorf("no .prev backup: %v", err)
	}
	entries, _ := os.ReadDir(e.dir)
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".new") || strings.Contains(en.Name(), "strip") {
			t.Errorf("temp file left behind: %s", en.Name())
		}
	}
}

func testWriteModes(t *testing.T, shell []string) {
	needStubbedIP(t, shell)
	e := setup(t, shell, "systemd")
	e.n.EnsureKey(ctx, "wg-lab")

	e.up(t, "wg-lab", true)
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Live}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.log(), "wg syncconf wg-lab") || strings.Contains(e.log(), "systemctl restart") {
		t.Errorf("live change should use syncconf only:\n%s", e.log())
	}
	if b, _ := os.ReadFile(filepath.Join(e.stub, "synced.wg-lab")); strings.Contains(string(b), "Address") {
		t.Errorf("syncconf got an unstripped config:\n%s", b)
	}

	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Restart}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.log(), "systemctl restart wg-quick@wg-lab") {
		t.Errorf("restart not used:\n%s", e.log())
	}

	// Without systemd, wg-quick does the work.
	e = setup(t, shell, "none")
	e.n.EnsureKey(ctx, "wg-lab")
	e.up(t, "wg-lab", false)
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Restart}); err != nil {
		t.Fatal(err)
	}
	if log := e.log(); !strings.Contains(log, "wg-quick down wg-lab\nwg-quick up wg-lab") || strings.Contains(log, "systemctl") {
		t.Errorf("want wg-quick down then up:\n%s", log)
	}

	// A failing unit reports its journal.
	e = setup(t, shell, "systemd")
	e.n.EnsureKey(ctx, "wg-lab")
	e.write(t, "fail", "")
	_, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Restart})
	if err == nil || !strings.Contains(err.Error(), "journal: handshake failed") || !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("err = %v", err)
	}
}

func testPSK(t *testing.T, shell []string) {
	e := setup(t, shell, "none")
	e.n.EnsureKey(ctx, "wg-lab")
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(true)}); err == nil || !strings.Contains(err.Error(), "no preshared key for nas") {
		t.Fatalf("missing psk: %v", err)
	}
	psk := "pskpskpskpskpskpskpskpskpskpskpskpskpskpsk="
	os.WriteFile(filepath.Join(e.dir, "wg-lab.psk.nas"), []byte(psk+"\n"), 0o600)
	pub, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(true)})
	if err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(e.dir, "wg-lab.conf"))
	if !strings.Contains(string(conf), "PresharedKey = "+psk+"\n") {
		t.Errorf("psk not filled in:\n%s", conf)
	}
	if strings.Contains(string(pub), psk) || wgconf.Hash(pub) != wgconf.Hash(rendered(true)) {
		t.Errorf("public form leaks or differs:\n%s", pub)
	}
	bad := bytes.Replace(rendered(true), []byte("@nas"), []byte("@../../x"), 1)
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: bad}); err == nil {
		t.Error("Write accepted a path-like peer name")
	}
}

func testRemove(t *testing.T, shell []string) {
	e := setup(t, shell, "systemd")
	e.n.EnsureKey(ctx, "wg-lab")
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.dir, "wg-lab.psk.nas"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(e.dir, "other.conf"), []byte("[Interface]\n"), 0o600)
	if err := e.n.Remove(ctx, "wg-lab"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(e.dir)
	if len(entries) != 1 || entries[0].Name() != "other.conf" {
		t.Errorf("left behind: %v", entries)
	}
	if !strings.Contains(e.log(), "systemctl disable --now --quiet wg-quick@wg-lab") {
		t.Errorf("unit not disabled:\n%s", e.log())
	}
	if err := e.n.Remove(ctx, "other"); err == nil || !strings.Contains(err.Error(), "not written by wglink") {
		t.Errorf("removed a hand-written config: %v", err)
	}
	if err := e.n.Remove(ctx, "wg-gone"); err != nil {
		t.Errorf("removing a missing interface should be a no-op: %v", err)
	}
}

func testProbe(t *testing.T, shell []string) {
	needStubbedIP(t, shell)
	e := setup(t, shell, "none")
	e.write(t, "addr", "1: lo    inet 127.0.0.1/8 scope host lo\\       valid_lft forever\n2: eth0@if7    inet 192.168.1.10/24 brd 192.168.1.255 scope global eth0\\       valid_lft forever\n3: wg-lab    inet 10.99.0.1/24 scope global wg-lab\\       valid_lft forever\n")
	e.write(t, "route4", "default via 192.168.1.1 dev eth0 proto dhcp\n192.168.1.0/24 dev eth0 proto kernel scope link src 192.168.1.10\nlocal 192.168.1.10 dev eth0 table local proto kernel scope host\n")
	e.write(t, "listen", "wg-lab\t51820\n")
	e.n.EnsureKey(ctx, "wg-lab")
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.dir, "wg0.conf"), []byte("[Interface]\nPrivateKey = priv000000000000000000000000000000000000009=\n"), 0o600)

	f, err := e.n.Probe(ctx, []string{"wg-lab", "wg0", "wg-new"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Have["wg"] || !f.Have["wg-quick"] || f.Systemd || f.Hostname == "" {
		t.Errorf("facts = %+v", f)
	}
	if len(f.Addrs) != 3 || f.Addrs[1].Iface != "eth0" || f.Addrs[1].Prefix.String() != "192.168.1.10/24" {
		t.Errorf("addrs = %v", f.Addrs)
	}
	if len(f.Routes) != 1 || f.Routes[0].Prefix.String() != "192.168.1.0/24" || f.Routes[0].Dev != "eth0" {
		t.Errorf("routes = %v", f.Routes)
	}
	if f.WGPorts["wg-lab"] != 51820 {
		t.Errorf("wg ports = %v", f.WGPorts)
	}
	if len(f.Managed) != 1 || f.Managed["wg-lab"] != "lab" {
		t.Errorf("managed = %v", f.Managed)
	}
	lab := f.Ifaces["wg-lab"]
	if lab.Conf != ConfManaged || !lab.Up || lab.PubKey != pubOf1 || wgconf.Hash(lab.Config) != wgconf.Hash(rendered(false)) {
		t.Errorf("wg-lab = %+v", lab)
	}
	if wg0 := f.Ifaces["wg0"]; wg0.Conf != ConfUnmanaged || wg0.Up {
		t.Errorf("wg0 = %+v", wg0)
	}
	if n := f.Ifaces["wg-new"]; n.Conf != ConfNone || n.PubKey != "" {
		t.Errorf("wg-new = %+v", n)
	}
}

func testStatus(t *testing.T, shell []string) {
	needStubbedIP(t, shell)
	e := setup(t, shell, "none")
	e.up(t, "wg-lab", false)
	e.write(t, "dump.wg-lab", "priv000000000000000000000000000000000000001=\t"+pubOf1+"\t51820\toff\n"+
		"peerkeyA\tpriv000000000000000000000000000000000000002=\t192.168.1.20:51820\t10.99.0.2/32\t1700000000\t1024\t2048\t25\n"+
		"peerkeyB\t(none)\t(none)\t10.99.0.3/32,192.168.50.0/24\t0\t0\t0\toff\n")
	e.write(t, "pingable", "10.99.0.2\n")
	st, err := e.n.Status(ctx, []string{"wg-lab", "wg-down"}, []PingTarget{
		{Iface: "wg-lab", Addr: netip.MustParseAddr("10.99.0.2")},
		{Iface: "wg-lab", Addr: netip.MustParseAddr("10.99.0.3")},
	})
	if err != nil {
		t.Fatal(err)
	}
	lab := st.Ifaces["wg-lab"]
	if !lab.Up || lab.PublicKey != pubOf1 || lab.ListenPort != 51820 || len(lab.Peers) != 2 {
		t.Fatalf("wg-lab = %+v", lab)
	}
	a, b := lab.Peers[0], lab.Peers[1]
	if !a.PresharedKey || a.Endpoint != "192.168.1.20:51820" || a.Handshake.Unix() != 1700000000 || a.RX != 1024 || a.TX != 2048 || a.Keepalive != 25 {
		t.Errorf("peer A = %+v", a)
	}
	if b.PresharedKey || b.Endpoint != "" || !b.Handshake.IsZero() || len(b.AllowedIPs) != 2 {
		t.Errorf("peer B = %+v", b)
	}
	if lab.Config != nil {
		t.Errorf("config reported for an interface with no file: %q", lab.Config)
	}
	if st.Ifaces["wg-down"].Up {
		t.Error("wg-down reported up")
	}
	if p := st.Pings[netip.MustParseAddr("10.99.0.2")]; !p.OK || p.RTT.Microseconds() != 420 {
		t.Errorf("ping .2 = %+v", p)
	}
	if p := st.Pings[netip.MustParseAddr("10.99.0.3")]; p.OK {
		t.Errorf("ping .3 = %+v", p)
	}
}

func TestStatusIncludesConfig(t *testing.T) {
	for name, shell := range shells(t) {
		t.Run(name, func(t *testing.T) {
			needStubbedIP(t, shell)
			e := setup(t, shell, "none")
			e.n.EnsureKey(ctx, "wg-lab")
			if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err != nil {
				t.Fatal(err)
			}
			e.write(t, "dump.wg-lab", "priv000000000000000000000000000000000000001=\t"+pubOf1+"\t51820\toff\n")
			st, err := e.n.Status(ctx, []string{"wg-lab"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if wgconf.Hash(st.Ifaces["wg-lab"].Config) != wgconf.Hash(rendered(false)) {
				t.Errorf("config = %q", st.Ifaces["wg-lab"].Config)
			}
		})
	}
}

func TestBuildQuotesValues(t *testing.T) {
	s, err := build("key", map[string]string{"WG_IFACE": "x'; rm -rf / #"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s), `WG_IFACE='x'\''; rm -rf / #'`) {
		t.Errorf("value not quoted:\n%s", s)
	}
	if !strings.HasPrefix(string(s), "set -eu\numask 077\n") || !strings.HasSuffix(string(s), "main\n") {
		t.Errorf("bad framing:\n%s", s)
	}
}

func TestParseRoutes(t *testing.T) {
	raw := []byte(`default via 192.168.1.1 dev eth0 proto dhcp src 192.168.1.10 metric 100
10.99.0.0/24 dev wg-lab proto kernel scope link src 10.99.0.1
100.64.0.0/10 dev tailscale0 table 52
unreachable 10.0.0.0/8 metric 1024
throw 172.16.0.0/12 table 52
local 10.99.0.1 dev wg-lab table local proto kernel scope host src 10.99.0.1
broadcast 192.168.1.255 dev eth0 table local proto kernel scope link
10.1.2.3 via 192.168.1.1 dev eth0
fe80::/64 dev eth0 proto kernel metric 256 pref medium
multicast ff00::/8 dev eth0 table local proto kernel metric 256 pref medium
`)
	var got []string
	for _, r := range parseRoutes(raw) {
		got = append(got, r.Prefix.String()+" "+r.Dev+" "+r.Table)
	}
	want := []string{
		"10.99.0.0/24 wg-lab ", "100.64.0.0/10 tailscale0 52", "10.0.0.0/8  ",
		"172.16.0.0/12  52", "10.1.2.3/32 eth0 ", "fe80::/64 eth0 ",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestParseUDP(t *testing.T) {
	raw := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  1: 00000000:CA6C 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 0 2 0000000000000000 0
  2: 3500007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 1 2 0000000000000000 0
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  3: 00000000000000000000000000000000:CA6C 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 0 2 0000000000000000 0
`)
	if got := parseUDP(raw); len(got) != 2 || got[0] != 51820 || got[1] != 53 {
		t.Errorf("ports = %v", got)
	}
}

func TestValidKey(t *testing.T) {
	for k, want := range map[string]bool{
		pubOf1: true,
		"q3F4sTqKG6c4kBxD7l6ZXpXwFfXoXJpPq2mWm3c1yE0=": true,
		"short=": false,
		"q3F4sTqKG6c4kBxD7l6ZXpXwFfXoXJpPq2mWm3c1yE0":  false,
		"q3F4sTqKG6c4kBxD7l6ZXpXwFfXoXJpPq2mWm3c1y;0=": false,
	} {
		if ValidKey(k) != want {
			t.Errorf("ValidKey(%q) = %v", k, !want)
		}
	}
}

func TestTruncatedOutput(t *testing.T) {
	if _, err := parseProbe([]byte("@probe 1\n@hostname x\n")); err == nil {
		t.Error("parseProbe accepted output without @end")
	}
	if _, err := parseStatus([]byte("@iface wg-lab down\n")); err == nil {
		t.Error("parseStatus accepted output without @end")
	}
}
