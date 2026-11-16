package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/wglink/internal/config"
	"github.com/gureetk/wglink/internal/node"
	"github.com/gureetk/wglink/internal/nodetest"
	"github.com/gureetk/wglink/internal/runner"
	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

var ctx = context.Background()

// lab is a set of fake nodes sharing one inventory directory.
type lab struct {
	t     *testing.T
	dir   string
	fakes map[string]*nodetest.Fake
}

func newLab(t *testing.T, names ...string) *lab {
	shell := nodetest.Shell(t)
	l := &lab{t: t, dir: t.TempDir(), fakes: map[string]*nodetest.Fake{}}
	for i, n := range names {
		l.fakes[n] = nodetest.New(t, shell, (i+1)*100)
	}
	return l
}

const base = `version: 1
nodes:
  pve1: {exec: [x], endpoint: 192.168.1.10, service: systemd}
  nas: {exec: [x], endpoint: 192.168.1.20, service: systemd}
  pi: {exec: [x], endpoint: 192.168.1.30, service: none}
networks:
`

// session loads yaml and the lab's state, wired to the fakes.
func (l *lab) session(yaml string) *Session {
	l.t.Helper()
	inv, err := config.Parse("wglink.yaml", []byte(yaml))
	if err != nil {
		l.t.Fatal(err)
	}
	st, err := state.Load(l.dir)
	if err != nil {
		l.t.Fatal(err)
	}
	s := New(l.dir, inv, st)
	s.newRunner = func(n *config.Node) (runner.Runner, error) { return l.fakes[n.Name].Runner(), nil }
	s.wgDir = func(name string) string { return l.fakes[name].Dir }
	return s
}

func (l *lab) plan(yaml string, opts Options) (*Session, *Plan) {
	l.t.Helper()
	s := l.session(yaml)
	p, err := s.Plan(ctx, opts)
	if err != nil {
		l.t.Fatal(err)
	}
	return s, p
}

// apply plans and applies, failing on problems.
func (l *lab) apply(yaml string, opts Options) []string {
	l.t.Helper()
	s, p := l.plan(yaml, opts)
	if len(p.Problems) > 0 {
		l.t.Fatalf("problems: %v", p.Problems)
	}
	var log []string
	if err := s.Apply(ctx, p, func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }); err != nil {
		l.t.Fatal(err)
	}
	return log
}

func (l *lab) conf(nodeName, iface string) string {
	b, _ := os.ReadFile(filepath.Join(l.fakes[nodeName].Dir, iface+".conf"))
	return string(b)
}

func members(p *Plan, network string) map[string]*MemberPlan {
	out := map[string]*MemberPlan{}
	for _, n := range p.Networks {
		if n.Name == network {
			for _, m := range n.Members {
				out[m.Node] = m
			}
		}
	}
	return out
}

func render(p *Plan) string {
	var b bytes.Buffer
	WritePlan(&b, p, Style{})
	return b.String()
}

const labNet = base + "  lab: {members: [pve1, nas]}\n"

func TestFreshNetwork(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	_, p := l.plan(labNet, Options{})
	if len(p.Problems) > 0 {
		t.Fatalf("problems: %v", p.Problems)
	}
	m := members(p, "lab")
	for _, n := range []string{"pve1", "nas"} {
		if m[n].Action != Create || !m[n].NewKey {
			t.Errorf("%s = %+v", n, m[n])
		}
	}
	if m["pve1"].Addr.String() != "10.99.0.1" || m["nas"].Addr.String() != "10.99.0.2" {
		t.Errorf("addresses %s %s", m["pve1"].Addr, m["nas"].Addr)
	}
	out := render(p)
	for _, want := range []string{"network lab   wg-lab   10.99.0.0/24   udp/51820", "+ pve1  10.99.0.1", "new interface, 1 peer, new key", "2 to create, 0 to change live, 0 to restart, 0 to remove."} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output lacks %q:\n%s", want, out)
		}
	}

	log := l.apply(labNet, Options{})
	if strings.Join(log, "\n") != "pve1: created a key for wg-lab\nnas: created a key for wg-lab\npve1: created wg-lab\nnas: created wg-lab" {
		t.Errorf("log:\n%s", strings.Join(log, "\n"))
	}
	st, _ := state.Load(l.dir)
	lab := st.Networks["lab"]
	if lab.Subnet.String() != "10.99.0.0/24" || lab.Port != 51820 || lab.Iface != "wg-lab" {
		t.Errorf("state = %+v", lab)
	}
	nas := lab.Members["nas"]
	if !wgconf.ValidKey(nas.PubKey) || nas.IP.String() != "10.99.0.2" || nas.Applied == "" {
		t.Errorf("nas state = %+v", nas)
	}
	pve := l.conf("pve1", "wg-lab")
	if !strings.Contains(pve, "PublicKey = "+nas.PubKey) || !strings.Contains(pve, "Endpoint = 192.168.1.20:51820") || !strings.Contains(pve, "AllowedIPs = 10.99.0.2/32") {
		t.Errorf("pve1 config:\n%s", pve)
	}
	if !strings.Contains(pve, "PrivateKey = priv") {
		t.Errorf("pve1 config lacks its private key:\n%s", pve)
	}
	if raw, _ := os.ReadFile(filepath.Join(l.dir, state.FileName)); strings.Contains(string(raw), "priv") {
		t.Error("a private key reached the state file")
	}

	_, again := l.plan(labNet, Options{})
	if !again.Empty() || Summary(again) != "Everything is up to date." {
		t.Errorf("second plan isn't empty:\n%s", render(again))
	}
}

func TestAddAndRemoveMembers(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(labNet, Options{})
	withPi := base + "  lab: {members: [pve1, nas, pi]}\n"

	_, p := l.plan(withPi, Options{})
	m := members(p, "lab")
	if m["pve1"].Action != Live || m["pve1"].Reasons[0] != "add peer pi" || m["pi"].Action != Create || m["pi"].Addr.String() != "10.99.0.3" {
		t.Fatalf("plan:\n%s", render(p))
	}
	log := l.apply(withPi, Options{})
	if !strings.Contains(strings.Join(log, "\n"), "pve1: updated live wg-lab") {
		t.Errorf("log: %v", log)
	}
	if !strings.Contains(l.fakes["pve1"].Log(), "wg syncconf wg-lab") {
		t.Error("adding a peer should use wg syncconf")
	}
	if _, p := l.plan(withPi, Options{}); !p.Empty() {
		t.Errorf("not converged:\n%s", render(p))
	}

	// Taking nas out removes its interface and its peer entries.
	withoutNas := base + "  lab: {members: [pve1, pi]}\n"
	_, p = l.plan(withoutNas, Options{})
	if len(p.Removals) != 1 || p.Removals[0].Node != "nas" || p.Removals[0].Reason != "no longer a member of lab" {
		t.Fatalf("removals: %+v", p.Removals)
	}
	l.apply(withoutNas, Options{})
	if _, err := os.Stat(filepath.Join(l.fakes["nas"].Dir, "wg-lab.conf")); !os.IsNotExist(err) {
		t.Error("nas still has its config")
	}
	if strings.Contains(l.conf("pve1", "wg-lab"), "# nas") {
		t.Error("pve1 still lists nas")
	}
	st, _ := state.Load(l.dir)
	if _, ok := st.Networks["lab"].Members["nas"]; ok {
		t.Error("state still lists nas")
	}
	// Addresses stay put: pi keeps .3.
	if st.Networks["lab"].Members["pi"].IP.String() != "10.99.0.3" {
		t.Errorf("pi renumbered: %+v", st.Networks["lab"].Members["pi"])
	}
}

func TestDeleteNetwork(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{})
	_, p := l.plan(base+"  {}\n", Options{})
	if len(p.Removals) != 2 || p.Removals[0].Reason != "network removed from wglink.yaml" {
		t.Fatalf("removals: %+v", p.Removals)
	}
	l.apply(base+"  {}\n", Options{})
	st, _ := state.Load(l.dir)
	if len(st.Networks) != 0 {
		t.Errorf("state = %+v", st.Networks)
	}
}

func TestDriftAndDownInterfaces(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{})
	path := filepath.Join(l.fakes["pve1"].Dir, "wg-lab.conf")
	conf, _ := os.ReadFile(path)
	os.WriteFile(path, append(conf, []byte("# a note\n")...), 0o600)
	l.fakes["nas"].SetDown("wg-lab")

	_, p := l.plan(labNet, Options{})
	m := members(p, "lab")
	if !m["pve1"].Drift || m["pve1"].Action != Live || !strings.Contains(strings.Join(m["pve1"].Reasons, ","), "edited on the node") {
		t.Errorf("pve1 = %+v", m["pve1"])
	}
	if m["nas"].Action != Start {
		t.Errorf("nas = %+v", m["nas"])
	}
	l.apply(labNet, Options{})
	if _, p := l.plan(labNet, Options{}); !p.Empty() {
		t.Errorf("not converged:\n%s", render(p))
	}
}

func TestAdopt(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	os.WriteFile(filepath.Join(l.fakes["pve1"].Dir, "wg-lab.conf"), []byte("[Interface]\nListenPort = 1\n"), 0o600)
	_, p := l.plan(labNet, Options{})
	if len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "--adopt") {
		t.Fatalf("problems: %v", p.Problems)
	}
	s, p := l.plan(labNet, Options{})
	if err := s.Apply(ctx, p, func(string, ...any) {}); err == nil {
		t.Error("Apply ran despite problems")
	}
	l.apply(labNet, Options{Adopt: true})
	if !wgconf.IsManaged([]byte(l.conf("pve1", "wg-lab"))) {
		t.Error("config not replaced")
	}
}

func TestUnreachableNode(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.fakes["nas"].Fail("ssh: connect to host nas port 22: Connection refused")
	_, p := l.plan(labNet, Options{})
	if len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "Connection refused") {
		t.Fatalf("problems: %v", p.Problems)
	}
	if members(p, "lab")["nas"].Action != Skip {
		t.Error("nas should be skipped")
	}
	// Leaving nas out makes it a warning, and pve1 can go ahead.
	_, p = l.plan(labNet, Options{Nodes: []string{"pve1"}})
	if len(p.Problems) != 0 || len(p.Warnings) == 0 {
		t.Fatalf("problems %v warnings %v", p.Problems, p.Warnings)
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "nas has no key yet") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	l.apply(labNet, Options{Nodes: []string{"pve1"}})
	// nas has no key, so pve1 leaves it out rather than write a bad one.
	if conf := l.conf("pve1", "wg-lab"); strings.Contains(conf, "# nas") || strings.Contains(conf, "new key") {
		t.Errorf("pve1 config:\n%s", conf)
	}
	// Once nas is reachable, the next apply joins them up.
	os.Remove(filepath.Join(l.fakes["nas"].Stub, "unreachable"))
	l.apply(labNet, Options{})
	if !strings.Contains(l.conf("pve1", "wg-lab"), "# nas") {
		t.Errorf("pve1 still lacks nas:\n%s", l.conf("pve1", "wg-lab"))
	}
}

func TestNodeFilterLeavesOthersAlone(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{Nodes: []string{"pve1"}})
	if _, err := os.Stat(filepath.Join(l.fakes["nas"].Dir, "wg-lab.conf")); !os.IsNotExist(err) {
		t.Error("nas was changed")
	}
	_, p := l.plan(labNet, Options{})
	if members(p, "lab")["nas"].Action != Create {
		t.Errorf("nas should still be pending:\n%s", render(p))
	}
}

func TestGoneNode(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{})
	st, _ := state.Load(l.dir)
	st.Net("lab").Member("old").IP = netip.MustParseAddr("10.99.0.9")
	st.Save(l.dir)
	_, p := l.plan(labNet, Options{})
	if len(p.Removals) != 1 || !p.Removals[0].Gone || len(p.Warnings) != 1 {
		t.Fatalf("removals %+v warnings %v", p.Removals, p.Warnings)
	}
	l.apply(labNet, Options{})
	st, _ = state.Load(l.dir)
	if _, ok := st.Networks["lab"].Members["old"]; ok {
		t.Error("state still lists old")
	}
}

func TestSubnetAvoidsNodeAddresses(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.fakes["nas"].WriteStub("addr", "2: eth0    inet 10.99.0.7/24 brd 10.99.0.255 scope global eth0\n")
	_, p := l.plan(labNet, Options{})
	if p.Networks[0].Subnet.String() != "10.99.1.0/24" {
		t.Errorf("subnet = %s", p.Networks[0].Subnet)
	}
	pinned := base + "  lab: {subnet: 10.99.0.0/24, members: [pve1, nas]}\n"
	_, p = l.plan(pinned, Options{})
	if len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "overlaps 10.99.0.0/24 (address on eth0) on nas") {
		t.Errorf("problems: %v", p.Problems)
	}
}

func TestPinnedAddressesAndKeepalive(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	yaml := `version: 1
nodes:
  pve1: {exec: [x], endpoint: 192.168.1.10, service: systemd}
  nas: {exec: [x], endpoint: "nas.lan:51900", service: systemd}
  pi: {exec: [x], endpoint: none, service: none, keepalive: 25}
networks:
  backup:
    subnet: 172.30.5.0/24
    port: 51910
    mtu: 1380
    members:
      nas: ".10"
      pve1: 172.30.5.20
      pi: auto
`
	l.apply(yaml, Options{})
	pi := l.conf("pi", "wg-backup")
	for _, want := range []string{"Address = 172.30.5.1/24", "ListenPort = 51910", "MTU = 1380", "Endpoint = nas.lan:51900", "PersistentKeepalive = 25", "AllowedIPs = 172.30.5.10/32"} {
		if !strings.Contains(pi, want) {
			t.Errorf("pi config lacks %q:\n%s", want, pi)
		}
	}
	nas, err := wgconf.Parse([]byte(l.conf("nas", "wg-backup")))
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range nas.Peers {
		if peer.Name == "pi" && peer.Endpoint != "" {
			t.Errorf("pi has endpoint none, but nas dials %s", peer.Endpoint)
		}
		if peer.Name == "pve1" && peer.Endpoint != "192.168.1.10:51910" {
			t.Errorf("nas dials pve1 at %q", peer.Endpoint)
		}
	}
}

func TestScopeErrors(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	s := l.session(labNet)
	if _, err := s.Plan(ctx, Options{Networks: []string{"nope"}}); err == nil {
		t.Error("unknown network accepted")
	}
	if _, err := s.Plan(ctx, Options{Nodes: []string{"nope"}}); err == nil {
		t.Error("unknown node accepted")
	}
}

func TestCheckNodes(t *testing.T) {
	l := newLab(t, "pve1")
	s := l.session(base + "  lab: {members: [pve1]}\n")
	p := &Plan{facts: map[string]*node.Facts{"pve1": {UID: 1000, OS: "debian", Systemd: true, Have: map[string]bool{}}}}
	s.checkNodes(p, map[string]bool{"pve1": true})
	got := strings.Join(p.Problems, "\n")
	for _, want := range []string{"wireguard-tools is not installed (apt install wireguard-tools)", "iproute2 is not installed", "needs root"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems lack %q:\n%s", want, got)
		}
	}
	if len(p.Warnings) != 0 {
		t.Errorf("warnings: %v (service is systemd, so no reboot warning)", p.Warnings)
	}
	if InstallHint("alpine", "x") != "apk add x" || InstallHint("plan9", "x") != "install x" {
		t.Error("installHint")
	}
}

func TestUsedSkipsOwnInterfaces(t *testing.T) {
	f := &node.Facts{
		Addrs: []node.Addr{
			{Iface: "lo", Prefix: netip.MustParsePrefix("127.0.0.1/8")},
			{Iface: "eth0", Prefix: netip.MustParsePrefix("192.168.1.10/24")},
			{Iface: "wg-lab", Prefix: netip.MustParsePrefix("10.99.0.1/24")},
			{Iface: "wg-old", Prefix: netip.MustParsePrefix("10.99.9.1/24")},
			{Iface: "eth0", Prefix: netip.MustParsePrefix("fe80::1/64")},
		},
		Routes:   []node.Route{{Prefix: netip.MustParsePrefix("10.99.0.0/24"), Dev: "wg-lab"}, {Prefix: netip.MustParsePrefix("100.64.0.0/10"), Dev: "tailscale0", Table: "52"}},
		UDPPorts: []uint16{53, 51820, 51830},
		WGPorts:  map[string]uint16{"wg-lab": 51820, "wg0": 51830},
		Managed:  map[string]string{"wg-old": "old"},
	}
	uses, ports := used(f, map[string]bool{"wg-lab": true})
	var got []string
	for _, u := range uses {
		got = append(got, u.Prefix.String()+" "+u.What)
	}
	if strings.Join(got, "|") != "192.168.1.0/24 address on eth0|100.64.0.0/10 route via tailscale0 in table 52" {
		t.Errorf("used = %v", got)
	}
	if fmt.Sprint(ports) != "[53 51830]" {
		t.Errorf("ports = %v", ports)
	}

	// An interface reporting port 0 holds the port in its config.
	f.WGPorts["wg-lab"] = 0
	f.Ifaces = map[string]*node.Iface{"wg-lab": {Up: true, Config: wgconf.Render(wgconf.Interface{Network: "lab", ListenPort: 51820})}}
	if _, ports := used(f, map[string]bool{"wg-lab": true}); fmt.Sprint(ports) != "[53 51830]" {
		t.Errorf("ports with wg reporting 0 = %v", ports)
	}
}

func TestRunnersForEachReach(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	inv, err := config.Parse("wglink.yaml", []byte(`version: 1
nodes:
  pve1: {ssh: root@192.168.1.10}
  ct101: {pct: {via: pve1, vmid: 101}, endpoint: none}
  vm200: {qm: {via: pve1, vmid: 200}, endpoint: none}
  web: {exec: [docker, exec, -i, web], endpoint: none}
  here: {local: true, sudo: true, endpoint: none}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := New(t.TempDir(), inv, state.New())
	s.Timeout = 45 * time.Second
	for name, want := range map[string]string{
		"pve1":  "ssh root@192.168.1.10",
		"ct101": "pct 101 on ssh root@192.168.1.10",
		"vm200": "qm 200 on ssh root@192.168.1.10",
		"web":   "exec docker exec -i web",
		"here":  "local",
	} {
		r, err := s.Runner(name)
		if err != nil || r.String() != want {
			t.Errorf("%s: %v, %v; want %s", name, r, err, want)
		}
	}
	if r, _ := s.Runner("vm200"); r.(runner.QM).Timeout != 45*time.Second {
		t.Errorf("vm200's agent timeout = %v", r.(runner.QM).Timeout)
	}
}
