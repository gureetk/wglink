package engine

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

func TestIPv6(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	auto := base + "  lab: {ipv6: auto, members: [pve1, nas, pi]}\n"
	_, p := l.plan(auto, Options{})
	sub := p.Networks[0].Subnet6
	if sub.Bits() != 64 || sub.Addr().As16()[0] != 0xfd {
		t.Fatalf("subnet6 = %s, want a unique local /64", sub)
	}
	l.apply(auto, Options{})
	st, _ := state.Load(l.dir)
	sub = st.Networks["lab"].Subnet6
	host := func(n byte) netip.Addr {
		b := sub.Addr().As16()
		b[15] = n
		return netip.AddrFrom16(b)
	}
	c := l.conf("pve1", "wg-lab")
	if !strings.Contains(c, "Address = 10.99.0.1/24, "+host(1).String()+"/64\n") || !strings.Contains(c, "AllowedIPs = 10.99.0.2/32, "+host(2).String()+"/128\n") {
		t.Errorf("pve1 with %s:\n%s", sub, c)
	}
	if p.Networks[0].Subnet6 != sub {
		t.Errorf("planned %s, applied %s", p.Networks[0].Subnet6, sub)
	}
	_, p = l.plan(auto, Options{})
	if out := render(p); !p.Empty() || !strings.Contains(out, "10.99.0.0/24, "+sub.String()) {
		t.Errorf("plan doesn't show the IPv6 subnet:\n%s", out)
	}

	// Spokes send the whole IPv6 subnet through the hub, which forwards it
	// if the kernel can do that for one interface, or warns.
	hub := strings.Replace(auto, "ipv6: auto,", "ipv6: auto, hub: pve1,", 1)
	_, p = l.plan(hub, Options{})
	// The fake nodes report this machine's settings.
	on, _ := os.ReadFile("/proc/sys/net/ipv6/conf/all/forwarding")
	_, err := os.Stat("/proc/sys/net/ipv6/conf/lo/force_forwarding")
	want := strings.TrimSpace(string(on)) != "1" && err != nil
	if got := strings.Contains(strings.Join(p.Warnings, "\n"), "pve1 can't pass IPv6 between the others until IPv6 forwarding is on there"); got != want {
		t.Errorf("warned %v, want %v: %v", got, want, p.Warnings)
	}
	l.apply(hub, Options{})
	if c := l.conf("nas", "wg-lab"); !strings.Contains(c, "AllowedIPs = 10.99.0.0/24, "+sub.String()+"\n") {
		t.Errorf("nas:\n%s", c)
	}
	if c := l.conf("pve1", "wg-lab"); !strings.Contains(c, "PostUp = "+wgconf.Forward6Hook+"\n") {
		t.Errorf("pve1:\n%s", c)
	}

	// A rebuilt state keeps the subnet.
	os.Remove(filepath.Join(l.dir, state.FileName))
	if _, err := l.session(hub).Rebuild(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := state.Load(l.dir); st.Networks["lab"].Subnet6 != sub {
		t.Errorf("rebuilt subnet6 = %s, want %s", st.Networks["lab"].Subnet6, sub)
	}
	if _, p := l.plan(hub, Options{}); !p.Empty() {
		t.Errorf("plan after rebuild:\n%s", render(p))
	}

	// Without IPv6 again, members restart to drop the address.
	off := strings.Replace(hub, "ipv6: auto, ", "", 1)
	_, p = l.plan(off, Options{})
	if m := members(p, "lab")["pve1"]; m.Action != Restart {
		t.Errorf("pve1 = %+v", m)
	}
	l.apply(off, Options{})
	if c := l.conf("pve1", "wg-lab"); strings.Contains(c, sub.Addr().String()[:8]) {
		t.Errorf("pve1 kept IPv6:\n%s", c)
	}
}
