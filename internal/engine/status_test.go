package engine

import (
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clock sets a fake's time and when it last saw a handshake, in seconds
// before that time; a negative ago means never.
func (l *lab) clock(name string, now, ago int64) {
	f := l.fakes[name]
	f.WriteStub("now", strconv.FormatInt(now, 10))
	if ago < 0 {
		f.WriteStub("handshake", "0")
	} else {
		f.WriteStub("handshake", strconv.FormatInt(now-ago, 10))
	}
}

func (l *lab) status(yaml string, opts StatusOptions) *Report {
	l.t.Helper()
	r, err := l.session(yaml).Status(ctx, opts)
	if err != nil {
		l.t.Fatal(err)
	}
	return r
}

func onlyLink(t *testing.T, r *Report) *Link {
	t.Helper()
	if len(r.Networks) != 1 || len(r.Networks[0].Links) != 1 {
		t.Fatalf("want one link, got %+v", r.Networks)
	}
	return r.Networks[0].Links[0]
}

func statusText(r *Report) string {
	var b bytes.Buffer
	WriteStatus(&b, r, Style{})
	return b.String()
}

func TestStatusHealthy(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{})
	l.clock("pve1", 1700000100, 14)
	l.clock("nas", 1700000100, 20)
	l.fakes["pve1"].WriteStub("pingable", "10.99.0.2\n")
	l.fakes["nas"].WriteStub("pingable", "10.99.0.1\n")

	r := l.status(labNet, StatusOptions{})
	k := onlyLink(t, r)
	if k.A != "pve1" || k.B != "nas" || k.State != LinkOK || k.Handshake != 14*time.Second || k.RTT != 420*time.Microsecond || k.RX != 1000 || k.TX != 2000 {
		t.Errorf("link = %+v", k)
	}
	if k.Note != "handshake 14s ago, ping 0.4 ms" {
		t.Errorf("note = %q", k.Note)
	}
	for _, m := range r.Networks[0].Members {
		if m.State != MemberUp || m.Drift {
			t.Errorf("member %+v", m)
		}
	}
	if !r.AllOK() || len(r.Errors) > 0 {
		t.Errorf("AllOK = %v, errors %v", r.AllOK(), r.Errors)
	}
	out := statusText(r)
	for _, want := range []string{
		"network lab   wg-lab   10.99.0.0/24   udp/51820",
		"  pve1  10.99.0.1       up",
		"  nas   10.99.0.2       up",
		"  pve1 <-> nas  ok  handshake 14s ago, ping 0.4 ms  rx 1000 B  tx 2.0 KiB",
		"The link is ok.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusJudgesLinks(t *testing.T) {
	cases := []struct {
		name             string
		agoA, agoB       int64 // seconds since each end's handshake; -1 for never
		pingable, noPing bool
		state            LinkState
		note             string
	}{
		{"never, no reply", -1, -1, false, false, LinkDown, "no handshake yet, no ping reply"},
		{"never, not pinged", -1, -1, false, true, LinkIdle, "no handshake yet"},
		{"idle, ping wakes it", -1, -1, true, false, LinkOK, "idle, ping 0.4 ms"},
		{"stale, no reply", 300, 400, false, false, LinkDown, "last handshake 5m ago, no ping reply"},
		{"stale, not pinged", 7200, -1, false, true, LinkIdle, "last handshake 2h ago"},
		{"fresh, ICMP blocked", 30, -1, false, false, LinkOK, "handshake 30s ago, no ping reply"},
		{"fresh by the other end", -1, 50, false, true, LinkOK, "handshake 50s ago"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newLab(t, "pve1", "nas")
			l.apply(labNet, Options{})
			l.clock("pve1", 1700000100, c.agoA)
			l.clock("nas", 1700000100, c.agoB)
			if c.pingable {
				l.fakes["pve1"].WriteStub("pingable", "10.99.0.2\n")
			}
			k := onlyLink(t, l.status(labNet, StatusOptions{NoPing: c.noPing}))
			if k.State != c.state || k.Note != c.note {
				t.Errorf("got %s %q, want %s %q", k.State, k.Note, c.state, c.note)
			}
		})
	}
}

func TestStatusUsesEachNodesClock(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{})
	// nas runs an hour ahead; both saw a handshake by their own clocks.
	l.clock("pve1", 1700000100, 200)
	l.clock("nas", 1700003700, 10)
	k := onlyLink(t, l.status(labNet, StatusOptions{NoPing: true}))
	if k.State != LinkOK || k.Handshake != 10*time.Second {
		t.Errorf("link = %+v", k)
	}
}

func TestStatusUnreachable(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{})
	l.clock("pve1", 1700000100, 5)
	l.fakes["nas"].Fail("ssh: connect to host nas port 22: Connection refused")

	// pve1 alone can vouch for the link.
	r := l.status(labNet, StatusOptions{NoPing: true})
	if k := onlyLink(t, r); k.State != LinkOK || k.Note != "handshake 5s ago" {
		t.Errorf("link = %+v", k)
	}
	if m := r.Networks[0].Members[1]; m.Node != "nas" || m.State != MemberUnreachable {
		t.Errorf("nas = %+v", m)
	}
	out := statusText(r)
	if !strings.Contains(out, "unreachable\n  nas: fake: ssh: connect to host nas port 22: Connection refused") {
		t.Errorf("output:\n%s", out)
	}

	l.fakes["pve1"].Fail("no route")
	r = l.status(labNet, StatusOptions{})
	if k := onlyLink(t, r); k.State != LinkUnknown || k.Note != "can't reach either end" {
		t.Errorf("link = %+v", k)
	}
	if r.AllOK() || !strings.Contains(statusText(r), "1 link: 1 unknown.") {
		t.Errorf("summary:\n%s", statusText(r))
	}
}

func TestStatusMissingAndDrift(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(labNet, Options{})

	l.fakes["nas"].SetDown("wg-lab")
	k := onlyLink(t, l.status(labNet, StatusOptions{NoPing: true}))
	if k.State != LinkMissing || k.Note != "wg-lab is down on nas" {
		t.Errorf("link = %+v", k)
	}

	// Someone deletes pve1 from nas's config by hand.
	l.fakes["nas"].SetUp("wg-lab", true)
	path := filepath.Join(l.fakes["nas"].Dir, "wg-lab.conf")
	conf, _ := os.ReadFile(path)
	cut := strings.Index(string(conf), "\n[Peer]")
	os.WriteFile(path, conf[:cut+1], 0o600)
	r := l.status(labNet, StatusOptions{NoPing: true})
	if k := onlyLink(t, r); k.State != LinkDown || k.Note != "nas doesn't list pve1 as a peer; run wglink apply" {
		t.Errorf("link = %+v", k)
	}
	m := r.Networks[0].Members
	if m[0].Drift || !m[1].Drift {
		t.Errorf("drift: pve1 %v, nas %v", m[0].Drift, m[1].Drift)
	}
	if out := statusText(r); !strings.Contains(out, "  nas   10.99.0.2       up, edited on the node since the last apply") {
		t.Errorf("output:\n%s", out)
	}

	// pi joins in wglink.yaml but isn't applied yet.
	three := base + "  lab: {members: [pve1, nas, pi]}\n"
	r = l.status(three, StatusOptions{NoPing: true})
	for _, k := range r.Networks[0].Links {
		if (k.A == "pi" || k.B == "pi") && (k.State != LinkMissing || k.Note != "pi isn't set up yet; run wglink apply") {
			t.Errorf("link = %+v", k)
		}
	}
	if m := r.Networks[0].Members[2]; m.State != MemberNew || m.Addr.IsValid() {
		t.Errorf("pi = %+v", m)
	}
	if out := statusText(r); !strings.Contains(out, "  pi    -               not applied yet") {
		t.Errorf("output:\n%s", out)
	}
}

func TestStatusScope(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	two := base + "  lab: {members: [pve1, nas]}\n  home: {members: [nas, pi]}\n"
	l.apply(two, Options{})
	before := l.fakes["pve1"].Log()
	r := l.status(two, StatusOptions{Networks: []string{"home"}, NoPing: true})
	if len(r.Networks) != 1 || r.Networks[0].Name != "home" || r.Networks[0].Iface != "wg-home" {
		t.Errorf("networks = %+v", r.Networks)
	}
	if l.fakes["pve1"].Log() != before {
		t.Error("asked pve1, which isn't in home")
	}
	if _, err := l.session(two).Status(ctx, StatusOptions{Networks: []string{"ghost"}}); err == nil {
		t.Error("unknown network accepted")
	}
}

func TestStatusEmpty(t *testing.T) {
	l := newLab(t, "pve1")
	if out := statusText(l.status(base, StatusOptions{})); !strings.Contains(out, "No networks yet") {
		t.Errorf("output:\n%s", out)
	}
	one := base + "  solo: {members: [pve1]}\n"
	if out := statusText(l.status(one, StatusOptions{})); !strings.Contains(out, "network solo   wg-solo   not applied yet") || !strings.Contains(out, "No links yet") {
		t.Errorf("output:\n%s", out)
	}
}

func TestLocalStatus(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	l.apply(labNet, Options{})
	f := l.fakes["pve1"]
	l.clock("pve1", 1700000100, 400)
	f.WriteStub("pingable", "10.99.0.2\n")
	// wg0 is someone else's, with one peer.
	f.SetUp("wg0", false)
	f.WriteStub("dump.wg0", "priv000000000000000000000000000000000000009=\tpub0wg0\t51000\toff\n"+
		"peerkeyAAAAAAAA=\t(none)\t203.0.113.5:51820\t10.8.0.0/24,10.8.0.1/32\t1700000090\t10\t20\t25\n")

	nd, _ := l.session(labNet).Node("pve1")
	ifaces, err := LocalStatus(ctx, nd, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ifaces) != 2 || ifaces[0].Name != "wg-lab" || ifaces[1].Name != "wg0" {
		t.Fatalf("interfaces = %+v", ifaces)
	}
	lab := ifaces[0]
	if lab.Network != "lab" || !lab.Up || lab.Port != 51820 || len(lab.Addresses) != 1 || lab.Addresses[0] != netip.MustParsePrefix("10.99.0.1/24") {
		t.Errorf("wg-lab = %+v", lab)
	}
	if p := lab.Peers[0]; p.Name != "nas" || p.Addr.String() != "10.99.0.2" || p.State != LinkOK || p.Note != "idle, ping 0.4 ms" {
		t.Errorf("nas = %+v", p)
	}
	if p := ifaces[1].Peers[0]; p.Name != "" || p.Addr.String() != "10.8.0.1" || p.State != LinkOK || p.Note != "handshake 10s ago, no ping reply" {
		t.Errorf("wg0 peer = %+v", p)
	}

	var b bytes.Buffer
	WriteLocalStatus(&b, ifaces, Style{})
	for _, want := range []string{
		"interface wg-lab   network lab   10.99.0.1/24   udp/51820\n  nas  ok  idle, ping 0.4 ms",
		"interface wg0   udp/51000\n  peerkeyAAA...  ok  handshake 10s ago, no ping reply  rx 10 B  tx 20 B",
		"All 2 peers are ok.",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}

	f.SetDown("wg-lab")
	ifaces, _ = LocalStatus(ctx, nd, false)
	if ifaces[0].Up || LocalOK(ifaces) || !strings.HasPrefix(LocalSummary(ifaces), "1 interface is down.") {
		t.Errorf("after wg-lab went down: %+v, %q", ifaces[0], LocalSummary(ifaces))
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[uint64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 40: "3.0 TiB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}
