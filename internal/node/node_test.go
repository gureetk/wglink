package node

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/wglink/internal/nodetest"
	"github.com/gureetk/wglink/internal/wgconf"
)

// env is a fake node and the Node that drives it.
type env struct {
	*nodetest.Fake
	n Node
}

func setup(t *testing.T, shell []string, service string) *env {
	t.Helper()
	f := nodetest.New(t, shell, 0)
	return &env{Fake: f, n: Node{Runner: f.Runner(), Dir: f.Dir, Service: service}}
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
	for name, shell := range nodetest.Shells(t) {
		t.Run(name, func(t *testing.T) {
			t.Run("key", func(t *testing.T) { testKey(t, shell) })
			t.Run("write", func(t *testing.T) { testWrite(t, shell) })
			t.Run("write modes", func(t *testing.T) { testWriteModes(t, shell) })
			t.Run("psk", func(t *testing.T) { testPSK(t, shell) })
			t.Run("remove", func(t *testing.T) { testRemove(t, shell) })
			t.Run("probe", func(t *testing.T) { testProbe(t, shell) })
			t.Run("status", func(t *testing.T) { testStatus(t, shell) })
			t.Run("revert", func(t *testing.T) { testRevert(t, shell) })
		})
	}
}

func testKey(t *testing.T, shell []string) {
	e := setup(t, shell, "none")
	pub, created, err := e.n.EnsureKey(ctx, "wg-lab")
	if err != nil || !created || pub != pubOf1 {
		t.Fatalf("EnsureKey = %q, %v, %v", pub, created, err)
	}
	fi, err := os.Stat(filepath.Join(e.Dir, "wg-lab.key"))
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
	conf, _ := os.ReadFile(filepath.Join(e.Dir, "wg-lab.conf"))
	if !strings.Contains(string(conf), "[Interface]\nPrivateKey = priv") {
		t.Errorf("private key not inserted after [Interface]:\n%s", conf)
	}
	if fi, _ := os.Stat(filepath.Join(e.Dir, "wg-lab.conf")); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", fi.Mode().Perm())
	}
	if log := e.Log(); !strings.Contains(log, "systemctl enable --quiet wg-quick@wg-lab") || !strings.Contains(log, "systemctl restart wg-quick@wg-lab") {
		t.Errorf("unit not enabled and started:\n%s", log)
	}
	// A second write keeps the old file as .prev.
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Live}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "wg-lab.conf.prev")); err != nil {
		t.Errorf("no .prev backup: %v", err)
	}
	entries, _ := os.ReadDir(e.Dir)
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".new") || strings.Contains(en.Name(), "strip") {
			t.Errorf("temp file left behind: %s", en.Name())
		}
	}
}

func testRevert(t *testing.T, shell []string) {
	nodetest.NeedStubbedIP(t, shell)
	e := setup(t, shell, "systemd")
	e.n.EnsureKey(ctx, "wg-lab")
	conf := filepath.Join(e.Dir, "wg-lab.conf")
	read := func() string { b, _ := os.ReadFile(conf); return string(b) }
	v1, v2 := rendered(false), bytes.Replace(rendered(false), []byte("51820"), []byte("51821"), 1)
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: v1}); err != nil {
		t.Fatal(err)
	}
	if e.Timer("wg-lab") {
		t.Fatal("a write without Revert armed a timer")
	}
	before := read()

	// Unconfirmed, the timer puts the old config back and restarts.
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: v2, Revert: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if !e.Timer("wg-lab") || !strings.Contains(e.Log(), "systemd-run --quiet --collect --unit=wglink-revert-wg-lab --on-active=60s") {
		t.Fatalf("no timer armed:\n%s", e.Log())
	}
	if !strings.Contains(read(), "ListenPort = 51821") {
		t.Fatalf("new config not written:\n%s", read())
	}
	e.FireRevert("wg-lab")
	if read() != before {
		t.Errorf("revert left:\n%s\nwant:\n%s", read(), before)
	}
	if !strings.HasSuffix(strings.TrimSpace(e.Log()), "systemctl restart wg-quick@wg-lab") {
		t.Errorf("revert didn't restart the unit:\n%s", e.Log())
	}
	if err := e.n.Confirm(ctx, "wg-lab"); err == nil || !strings.Contains(err.Error(), "already undid the change") {
		t.Errorf("confirm after the timer fired: %v", err)
	}

	// Confirmed, the change stays and nothing is left behind.
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: v2, Revert: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := e.n.Confirm(ctx, "wg-lab"); err != nil {
		t.Fatal(err)
	}
	if e.Timer("wg-lab") || !strings.Contains(read(), "ListenPort = 51821") {
		t.Errorf("after confirm: timer %v, config:\n%s", e.Timer("wg-lab"), read())
	}
	for _, f := range []string{"wg-lab.conf.revert", ".wg-lab.revert"} {
		if _, err := os.Stat(filepath.Join(e.Dir, f)); err == nil {
			t.Errorf("%s left behind", f)
		}
	}

	// A new interface that isn't confirmed is taken down again.
	e.n.EnsureKey(ctx, "wg-new")
	if _, err := e.n.Write(ctx, Write{Iface: "wg-new", Config: v1, Revert: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	e.FireRevert("wg-new")
	if _, err := os.Stat(filepath.Join(e.Dir, "wg-new.conf")); err == nil || !strings.Contains(e.Log(), "systemctl disable --now wg-quick@wg-new") {
		t.Errorf("new interface not taken down:\n%s", e.Log())
	}

	// Without systemd there's no timer, so the write refuses.
	none := setup(t, shell, "none")
	none.n.EnsureKey(ctx, "wg-lab")
	if _, err := none.n.Write(ctx, Write{Iface: "wg-lab", Config: v1, Revert: time.Minute}); err == nil || !strings.Contains(err.Error(), "needs systemd") {
		t.Errorf("revert without systemd: %v", err)
	}
}

func testWriteModes(t *testing.T, shell []string) {
	nodetest.NeedStubbedIP(t, shell)
	e := setup(t, shell, "systemd")
	e.n.EnsureKey(ctx, "wg-lab")

	e.SetUp("wg-lab", true)
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Live}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Log(), "wg syncconf wg-lab") || strings.Contains(e.Log(), "systemctl restart") {
		t.Errorf("live change should use syncconf only:\n%s", e.Log())
	}
	// Settings the config leaves out are zeroed, or syncconf would keep them.
	b, _ := os.ReadFile(filepath.Join(e.Stub, "synced.wg-lab"))
	if strings.Contains(string(b), "Address") || !strings.HasSuffix(string(b), "PresharedKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nPersistentKeepalive = 0\n") {
		t.Errorf("syncconf got:\n%s", b)
	}

	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Restart}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Log(), "systemctl restart wg-quick@wg-lab") {
		t.Errorf("restart not used:\n%s", e.Log())
	}

	// Without systemd, wg-quick does the work.
	e = setup(t, shell, "none")
	e.n.EnsureKey(ctx, "wg-lab")
	e.SetUp("wg-lab", false)
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Restart}); err != nil {
		t.Fatal(err)
	}
	if log := e.Log(); !strings.Contains(log, "wg-quick down wg-lab\nwg-quick up wg-lab") || strings.Contains(log, "systemctl") {
		t.Errorf("want wg-quick down then up:\n%s", log)
	}

	// A failing unit reports its journal.
	e = setup(t, shell, "systemd")
	e.n.EnsureKey(ctx, "wg-lab")
	e.WriteStub("fail", "")
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
	psk := "pskpskpskpskpskpskpskpskpskpskpskpskpskpsk0="
	other, _ := wgconf.GeneratePSK()
	if err := e.n.SetPSKs(ctx, "wg-lab", map[string]string{"nas": psk, "pi": other}); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"nas", "pi"} {
		if fi, err := os.Stat(filepath.Join(e.Dir, "wg-lab.psk."+peer)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s's key file: %v %v", peer, fi, err)
		}
	}
	pub, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(true)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "wg-lab.psk.pi")); !os.IsNotExist(err) {
		t.Errorf("a key the config doesn't use was kept: %v", err)
	}
	f, err := e.n.Probe(ctx, []string{"wg-lab"})
	if err != nil {
		t.Fatal(err)
	}
	if i := f.Ifaces["wg-lab"]; len(i.PSKFile) != 1 || i.PSKFile["nas"] != wgconf.PSKHash(psk) || i.PSKConf["nas"] != wgconf.PSKHash(psk) {
		t.Errorf("probe: files %v, config %v", i.PSKFile, i.PSKConf)
	}
	// Stored, not yet loaded.
	e.n.SetPSKs(ctx, "wg-lab", map[string]string{"nas": other})
	if f, _ := e.n.Probe(ctx, []string{"wg-lab"}); f.Ifaces["wg-lab"].PSKFile["nas"] != wgconf.PSKHash(other) || f.Ifaces["wg-lab"].PSKConf["nas"] != wgconf.PSKHash(psk) {
		t.Errorf("probe after storing a new key: %+v", f.Ifaces["wg-lab"])
	}
	for _, bad := range []map[string]string{{"../x": psk}, {"nas": "short"}, {"nas": "x' y"}} {
		if err := e.n.SetPSKs(ctx, "wg-lab", bad); err == nil {
			t.Errorf("SetPSKs accepted %v", bad)
		}
	}
	e.n.SetPSKs(ctx, "wg-lab", map[string]string{"nas": psk})
	conf, _ := os.ReadFile(filepath.Join(e.Dir, "wg-lab.conf"))
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
	os.WriteFile(filepath.Join(e.Dir, "wg-lab.psk.nas"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(e.Dir, "other.conf"), []byte("[Interface]\n"), 0o600)
	if err := e.n.Remove(ctx, "wg-lab"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(e.Dir)
	if len(entries) != 1 || entries[0].Name() != "other.conf" {
		t.Errorf("left behind: %v", entries)
	}
	if !strings.Contains(e.Log(), "systemctl disable --now --quiet wg-quick@wg-lab") {
		t.Errorf("unit not disabled:\n%s", e.Log())
	}
	if err := e.n.Remove(ctx, "other"); err == nil || !strings.Contains(err.Error(), "not written by wglink") {
		t.Errorf("removed a hand-written config: %v", err)
	}
	if err := e.n.Remove(ctx, "wg-gone"); err != nil {
		t.Errorf("removing a missing interface should be a no-op: %v", err)
	}
}

func testProbe(t *testing.T, shell []string) {
	nodetest.NeedStubbedIP(t, shell)
	e := setup(t, shell, "none")
	e.WriteStub("addr", "1: lo    inet 127.0.0.1/8 scope host lo\\       valid_lft forever\n2: eth0@if7    inet 192.168.1.10/24 brd 192.168.1.255 scope global eth0\\       valid_lft forever\n3: wg-lab    inet 10.99.0.1/24 scope global wg-lab\\       valid_lft forever\n")
	e.WriteStub("route4", "default via 192.168.1.1 dev eth0 proto dhcp\n192.168.1.0/24 dev eth0 proto kernel scope link src 192.168.1.10\nlocal 192.168.1.10 dev eth0 table local proto kernel scope host\n")
	e.WriteStub("listen", "wg-lab\t51820\n")
	e.n.EnsureKey(ctx, "wg-lab")
	if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.Dir, "wg0.conf"), []byte("[Interface]\nPrivateKey = priv000000000000000000000000000000000000009=\n"), 0o600)

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
	nodetest.NeedStubbedIP(t, shell)
	e := setup(t, shell, "none")
	e.SetUp("wg-lab", false)
	e.WriteStub("dump.wg-lab", "priv000000000000000000000000000000000000001=\t"+pubOf1+"\t51820\toff\n"+
		"peerkeyA\tpriv000000000000000000000000000000000000002=\t192.168.1.20:51820\t10.99.0.2/32\t1700000000\t1024\t2048\t25\n"+
		"peerkeyB\t(none)\t(none)\t10.99.0.3/32,192.168.50.0/24\t0\t0\t0\toff\n")
	e.WriteStub("pingable", "10.99.0.2\n")
	e.WriteStub("now", "1700000100\n")
	two, three := PingTarget{"wg-lab", netip.MustParseAddr("10.99.0.2")}, PingTarget{"wg-lab", netip.MustParseAddr("10.99.0.3")}
	st, err := e.n.Status(ctx, []string{"wg-lab", "wg-down"}, []PingTarget{two, three})
	if err != nil {
		t.Fatal(err)
	}
	if st.Now.Unix() != 1700000100 {
		t.Errorf("now = %v", st.Now)
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
	if p := st.Pings[two]; !p.OK || p.RTT.Microseconds() != 420 {
		t.Errorf("ping .2 = %+v", p)
	}
	if p, ok := st.Pings[three]; !ok || p.OK {
		t.Errorf("ping .3 = %+v, %v", p, ok)
	}
}

func TestStatusAllInterfaces(t *testing.T) {
	for name, shell := range nodetest.Shells(t) {
		t.Run(name, func(t *testing.T) {
			nodetest.NeedStubbedIP(t, shell)
			e := setup(t, shell, "none")
			// wg-lab is wglink's but down; wg0 is up but someone else's;
			// other.conf is neither wglink's nor up.
			e.n.EnsureKey(ctx, "wg-lab")
			if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err != nil {
				t.Fatal(err)
			}
			e.SetDown("wg-lab")
			e.SetUp("wg0", false)
			e.WriteStub("dump.wg0", "priv000000000000000000000000000000000000009=\tpub0wg0\t51000\toff\n")
			os.WriteFile(filepath.Join(e.Dir, "other.conf"), []byte("[Interface]\n"), 0o600)
			st, err := e.n.Status(ctx, AllInterfaces, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Ifaces) != 2 {
				t.Fatalf("interfaces = %v", st.Ifaces)
			}
			if lab := st.Ifaces["wg-lab"]; lab == nil || lab.Up || wgconf.Hash(lab.Config) != wgconf.Hash(rendered(false)) {
				t.Errorf("wg-lab = %+v", lab)
			}
			if wg0 := st.Ifaces["wg0"]; wg0 == nil || !wg0.Up || wg0.ListenPort != 51000 || wg0.Config != nil {
				t.Errorf("wg0 = %+v", wg0)
			}
		})
	}
}

func TestStatusIncludesConfig(t *testing.T) {
	for name, shell := range nodetest.Shells(t) {
		t.Run(name, func(t *testing.T) {
			nodetest.NeedStubbedIP(t, shell)
			e := setup(t, shell, "none")
			e.n.EnsureKey(ctx, "wg-lab")
			if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err != nil {
				t.Fatal(err)
			}
			e.WriteStub("dump.wg-lab", "priv000000000000000000000000000000000000001=\t"+pubOf1+"\t51820\toff\n")
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

func TestTruncatedOutput(t *testing.T) {
	if _, err := parseProbe([]byte("@probe 1\n@hostname x\n")); err == nil {
		t.Error("parseProbe accepted output without @end")
	}
	if _, err := parseStatus([]byte("@iface wg-lab down\n")); err == nil {
		t.Error("parseStatus accepted output without @end")
	}
}

// A config edited by hand may lack its last newline; the directive after it
// must still be found.
func TestConfigWithoutFinalNewline(t *testing.T) {
	for name, shell := range nodetest.Shells(t) {
		t.Run(name, func(t *testing.T) {
			nodetest.NeedStubbedIP(t, shell)
			e := setup(t, shell, "none")
			text := strings.TrimSuffix(string(rendered(false)), "\n")
			os.WriteFile(filepath.Join(e.Dir, "wg-lab.conf"), []byte(text), 0o600)
			f, err := e.n.Probe(ctx, []string{"wg-lab"})
			if err != nil {
				t.Fatal(err)
			}
			if c := f.Ifaces["wg-lab"]; c.Conf != ConfManaged || wgconf.Hash(c.Config) != wgconf.Hash(rendered(false)) {
				t.Errorf("probe = %+v", c)
			}
			st, err := e.n.Status(ctx, []string{"wg-lab"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if wgconf.Hash(st.Ifaces["wg-lab"].Config) != wgconf.Hash(rendered(false)) {
				t.Errorf("status config = %q", st.Ifaces["wg-lab"].Config)
			}
		})
	}
}

func TestRotateKey(t *testing.T) {
	for name, shell := range nodetest.Shells(t) {
		t.Run(name, func(t *testing.T) {
			nodetest.NeedStubbedIP(t, shell)
			e := setup(t, shell, "none")
			old, _, _ := e.n.EnsureKey(ctx, "wg-lab")
			if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false)}); err != nil {
				t.Fatal(err)
			}
			f, _ := e.n.Probe(ctx, []string{"wg-lab"})
			if i := f.Ifaces["wg-lab"]; i.ConfPub != old || i.LivePub != old || i.StaleKey() {
				t.Fatalf("before rotating: %+v", i)
			}

			pub, err := e.n.RotateKey(ctx, "wg-lab")
			if err != nil || pub == old || !wgconf.ValidKey(pub) {
				t.Fatalf("RotateKey = %s, %v (old %s)", pub, err, old)
			}
			// Until the config is written again, it and the interface lag behind.
			f, _ = e.n.Probe(ctx, []string{"wg-lab"})
			if i := f.Ifaces["wg-lab"]; i.PubKey != pub || i.ConfPub != old || !i.StaleKey() {
				t.Errorf("after rotating: %+v", i)
			}
			if _, err := e.n.Write(ctx, Write{Iface: "wg-lab", Config: rendered(false), Mode: wgconf.Live}); err != nil {
				t.Fatal(err)
			}
			f, _ = e.n.Probe(ctx, []string{"wg-lab"})
			if i := f.Ifaces["wg-lab"]; i.StaleKey() {
				t.Errorf("after writing: %+v", i)
			}
		})
	}
}
