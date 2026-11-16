package wgconf

import (
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

const (
	keyNas   = "Lk8WlXg1sLk0v4LZpVdXyJ6aF1b2c3d4e5f6g7h8i9A="
	keyCt101 = "7HqzR2nXbS0eXq4m5YtZ8wVq1a2b3c4d5e6f7g8h9iA="
)

func example() Interface {
	return Interface{
		Network:    "lab",
		Addresses:  []netip.Prefix{pfx("10.99.0.1/24")},
		ListenPort: 51820,
		Peers: []Peer{
			{Name: "nas", PublicKey: keyNas, AllowedIPs: []netip.Prefix{pfx("10.99.0.10/32")}, Endpoint: "nas.lan:51820"},
			{Name: "ct101", PublicKey: keyCt101, AllowedIPs: []netip.Prefix{pfx("10.99.0.2/32")}, Endpoint: "192.168.1.101:51820", Keepalive: 25},
		},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s mismatch; got:\n%s\nwant:\n%s", name, got, want)
	}
}

func TestRenderGolden(t *testing.T) {
	golden(t, "pve1.conf", Render(example()))

	i := example()
	i.MTU = 1380
	i.ListenPort = 0
	i.Peers[0].PresharedKey = true
	i.Peers[1].Endpoint = ""
	golden(t, "variants.conf", Render(i))
}

func TestRenderSortsPeers(t *testing.T) {
	out := string(Render(example()))
	if strings.Index(out, "# ct101") > strings.Index(out, "# nas") {
		t.Errorf("peers not sorted by name:\n%s", out)
	}
}

func TestParseRoundTrip(t *testing.T) {
	want := example()
	got, err := Parse(Render(want))
	if err != nil {
		t.Fatal(err)
	}
	// Render sorts peers.
	want.Peers[0], want.Peers[1] = want.Peers[1], want.Peers[0]
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if got.Network != "lab" || !IsManaged(Render(want)) {
		t.Error("marker not recognised")
	}
}

func TestParseHandWritten(t *testing.T) {
	text := `[interface]
privatekey = aGlkZGVuaGlkZGVuaGlkZGVuaGlkZGVuaGlkZGVuMDA=
address = 10.0.0.1/24, fd00::1/64   # dual stack
listenport=51820
DNS = 1.1.1.1
PostUp = echo hi

[PEER]
publickey = ` + keyNas + `
AllowedIPs = 10.0.0.2, 192.168.50.7/24
Endpoint = [fd00::2]:51820
PersistentKeepalive = off
`
	i, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if IsManaged([]byte(text)) || i.Network != "" {
		t.Error("hand-written config looks managed")
	}
	if len(i.Addresses) != 2 || i.Addresses[1] != pfx("fd00::1/64") || i.ListenPort != 51820 {
		t.Errorf("interface = %+v", i)
	}
	p := i.Peers[0]
	if p.PublicKey != keyNas || p.Endpoint != "[fd00::2]:51820" || p.Keepalive != 0 {
		t.Errorf("peer = %+v", p)
	}
	if !samePrefixes(p.AllowedIPs, []netip.Prefix{pfx("10.0.0.2/32"), pfx("192.168.50.0/24")}) {
		t.Errorf("allowed IPs = %v (bare address should be /32, host bits masked)", p.AllowedIPs)
	}
}

func TestParseErrors(t *testing.T) {
	for _, text := range []string{
		"[Interface]\nAddress = nonsense\n",
		"[Interface]\nListenPort = 70000\n",
		"Address = 10.0.0.1/24\n",
		"[Peer]\nno equals sign\n",
		"[Peer]\nPersistentKeepalive = soon\n",
	} {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("Parse(%q) succeeded", text)
		}
	}
}

func TestPublicAndHash(t *testing.T) {
	onNode := "# Managed by wglink (network lab). Edits here are replaced on the next apply.\n[Interface]\nPrivateKey = c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0MDA=\nAddress = 10.99.0.1/24\n\n[Peer]\n# nas\nPublicKey = " + keyNas + "\nPresharedKey = cHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrMDA=\nAllowedIPs = 10.99.0.10/32\n"
	rendered := "# Managed by wglink (network lab). Edits here are replaced on the next apply.\n[Interface]\nAddress = 10.99.0.1/24\n\n[Peer]\n# nas\nPublicKey = " + keyNas + "\nPresharedKey = @nas\nAllowedIPs = 10.99.0.10/32\n"
	pub := string(Public([]byte(onNode)))
	if strings.Contains(pub, "PrivateKey") || strings.Contains(pub, "cHNr") {
		t.Errorf("secrets survived:\n%s", pub)
	}
	if !strings.Contains(pub, "PresharedKey = (set)\n") {
		t.Errorf("preshared key not masked:\n%s", pub)
	}
	if Hash([]byte(onNode)) != Hash([]byte(rendered)) {
		t.Errorf("hash differs between node file and rendered template:\n%s\n---\n%s", pub, Public([]byte(rendered)))
	}
	if Hash([]byte(rendered)) == Hash([]byte(strings.Replace(rendered, "10.99.0.10", "10.99.0.11", 1))) {
		t.Error("hash ignores content")
	}
}

func TestCompare(t *testing.T) {
	base := example()
	clone := func() Interface {
		c := base
		c.Addresses = append([]netip.Prefix(nil), base.Addresses...)
		c.Peers = append([]Peer(nil), base.Peers...)
		return c
	}
	for _, tc := range []struct {
		name   string
		edit   func(*Interface)
		change Change
		why    string
	}{
		{"same", func(*Interface) {}, Same, ""},
		{"same peers reordered", func(i *Interface) { i.Peers[0], i.Peers[1] = i.Peers[1], i.Peers[0] }, Same, ""},
		{"add peer", func(i *Interface) {
			i.Peers = append(i.Peers, Peer{Name: "pi", PublicKey: "k", AllowedIPs: []netip.Prefix{pfx("10.99.0.3/32")}})
		}, Live, "add peer pi"},
		{"remove peer", func(i *Interface) { i.Peers = i.Peers[:1] }, Live, "remove peer ct101"},
		{"endpoint", func(i *Interface) { i.Peers[0].Endpoint = "10.0.0.9:51820" }, Live, "peer nas: endpoint nas.lan:51820 -> 10.0.0.9:51820"},
		{"key", func(i *Interface) { i.Peers[0].PublicKey = "other" }, Live, "peer nas: new key"},
		{"keepalive", func(i *Interface) { i.Peers[0].Keepalive = 25 }, Live, "peer nas: keepalive 0 -> 25"},
		{"psk", func(i *Interface) { i.Peers[0].PresharedKey = true }, Live, "peer nas: preshared key on"},
		{"address", func(i *Interface) { i.Addresses = []netip.Prefix{pfx("10.99.0.5/24")} }, Restart, "address 10.99.0.1/24 -> 10.99.0.5/24"},
		{"port", func(i *Interface) { i.ListenPort = 51821 }, Restart, "port 51820 -> 51821"},
		{"mtu", func(i *Interface) { i.MTU = 1380 }, Restart, "mtu auto -> 1380"},
		{"new route", func(i *Interface) {
			i.Peers[0].AllowedIPs = []netip.Prefix{pfx("10.99.0.10/32"), pfx("192.168.50.0/24")}
		}, Restart, "routes changed"},
		{"peer moves inside subnet", func(i *Interface) {
			i.Peers[0].AllowedIPs = []netip.Prefix{pfx("10.99.0.11/32")}
		}, Live, "peer nas: allowed IPs 10.99.0.10/32 -> 10.99.0.11/32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := clone()
			tc.edit(&n)
			change, why := Compare(base, n)
			if change != tc.change {
				t.Errorf("change = %v, want %v (why %v)", change, tc.change, why)
			}
			if tc.why != "" && !strings.Contains(strings.Join(why, "\n"), tc.why) {
				t.Errorf("why = %v, want %q", why, tc.why)
			}
			if tc.change == Same && len(why) != 0 {
				t.Errorf("why = %v, want none", why)
			}
		})
	}
}
