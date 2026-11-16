package ipam

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/config"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func ip(s string) netip.Addr    { return netip.MustParseAddr(s) }

func spec(t *testing.T, s string) config.AddrSpec {
	t.Helper()
	a, err := config.ParseAddrSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func members(names ...string) []Member {
	var out []Member
	for _, n := range names {
		out = append(out, Member{Node: n})
	}
	return out
}

func base(nets ...Net) Request {
	return Request{Pool: pfx("10.99.0.0/16"), Prefix: 24, BasePort: 51820, Networks: nets}
}

// summary renders results as "net subnet port node=addr ..." lines.
func summary(res map[string]*Assigned) string {
	var lines []string
	for name, a := range res {
		var addrs []string
		for node, ip := range a.Addrs {
			addrs = append(addrs, node+"="+ip.String())
		}
		sort.Strings(addrs)
		lines = append(lines, fmt.Sprintf("%s %s %d %s", name, a.Subnet, a.Port, strings.Join(addrs, " ")))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func resolve(t *testing.T, req Request) string {
	t.Helper()
	res, err := Resolve(req)
	if err != nil {
		t.Fatal(err)
	}
	return summary(res)
}

func wantErr(t *testing.T, req Request, want string) {
	t.Helper()
	_, err := Resolve(req)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to contain %q", err, want)
	}
}

func TestAutoEverything(t *testing.T) {
	got := resolve(t, base(
		Net{Name: "lab", Members: members("pve1", "nas", "pi")},
		Net{Name: "backup", Members: members("nas", "pve1")},
	))
	want := "backup 10.99.0.0/24 51820 nas=10.99.0.1 pve1=10.99.0.2\n" +
		"lab 10.99.1.0/24 51821 nas=10.99.1.2 pi=10.99.1.3 pve1=10.99.1.1"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestInputOrderDoesNotMatter(t *testing.T) {
	a := Net{Name: "a", Members: members("x", "y")}
	b := Net{Name: "b", Members: members("y", "z")}
	if resolve(t, base(a, b)) != resolve(t, base(b, a)) {
		t.Error("result depends on input order")
	}
}

func TestStickySubnetPortAndAddrs(t *testing.T) {
	prev := Assigned{
		Subnet: pfx("10.99.7.0/24"),
		Port:   51830,
		Addrs:  map[string]netip.Addr{"nas": ip("10.99.7.5"), "pi": ip("10.99.7.2")},
	}
	got := resolve(t, base(Net{Name: "lab", Members: members("pve1", "nas", "pi"), Prev: prev}))
	want := "lab 10.99.7.0/24 51830 nas=10.99.7.5 pi=10.99.7.2 pve1=10.99.7.1"
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestRemovingAMemberRenumbersNobody(t *testing.T) {
	prev := Assigned{Subnet: pfx("10.99.0.0/24"), Port: 51820, Addrs: map[string]netip.Addr{
		"a": ip("10.99.0.1"), "b": ip("10.99.0.2"), "c": ip("10.99.0.3"),
	}}
	got := resolve(t, base(Net{Name: "n", Members: members("a", "c"), Prev: prev}))
	if !strings.Contains(got, "a=10.99.0.1 c=10.99.0.3") {
		t.Errorf("got %s", got)
	}
}

func TestPinsWinOverRecordedAddresses(t *testing.T) {
	prev := Assigned{Subnet: pfx("10.99.0.0/24"), Port: 51820, Addrs: map[string]netip.Addr{
		"a": ip("10.99.0.10"), "b": ip("10.99.0.2"),
	}}
	n := Net{Name: "n", Members: []Member{{Node: "a"}, {Node: "b", Addr: spec(t, ".10")}}, Prev: prev}
	got := resolve(t, base(n))
	if got != "n 10.99.0.0/24 51820 a=10.99.0.1 b=10.99.0.10" {
		t.Errorf("got %s", got)
	}
}

func TestFixedSubnetAndHostParts(t *testing.T) {
	n := Net{Name: "backup", Subnet: pfx("172.30.0.0/16"), Port: 51900, Members: []Member{
		{Node: "nas", Addr: spec(t, ".5.10")},
		{Node: "pve1", Addr: spec(t, "172.30.0.20")},
		{Node: "ct101"},
	}}
	got := resolve(t, base(n))
	if got != "backup 172.30.0.0/16 51900 ct101=172.30.0.1 nas=172.30.5.10 pve1=172.30.0.20" {
		t.Errorf("got %s", got)
	}
}

func TestAvoidsAddressesAndRoutesOnMembers(t *testing.T) {
	req := base(Net{Name: "lab", Members: members("a", "b")})
	req.Used = map[string][]Use{
		"a": {{Prefix: pfx("10.99.0.0/24"), What: "address on eth0"}},
		"b": {{Prefix: pfx("10.99.1.128/25"), What: "route via eth1"}},
	}
	if got := resolve(t, req); !strings.HasPrefix(got, "lab 10.99.2.0/24") {
		t.Errorf("got %s", got)
	}
}

func TestSubnetConflicts(t *testing.T) {
	used := map[string][]Use{"a": {{Prefix: pfx("192.168.1.0/24"), What: "address on eth0"}}}

	req := base(Net{Name: "lab", Subnet: pfx("192.168.0.0/16"), Members: members("a", "b")})
	req.Used = used
	wantErr(t, req, "subnet 192.168.0.0/16 overlaps 192.168.1.0/24 (address on eth0) on a")

	req = base(Net{Name: "lab", Members: members("a", "b"), Prev: Assigned{Subnet: pfx("192.168.1.0/24")}})
	req.Used = used
	wantErr(t, req, "picked earlier")

	wantErr(t, base(
		Net{Name: "x", Subnet: pfx("10.1.0.0/24"), Members: members("a", "b")},
		Net{Name: "y", Subnet: pfx("10.1.0.0/25"), Members: members("b", "c")},
	), "overlaps network x")
}

func TestOverlapAllowedWithoutSharedNodes(t *testing.T) {
	resolve(t, base(
		Net{Name: "x", Subnet: pfx("10.1.0.0/24"), Members: members("a", "b")},
		Net{Name: "y", Subnet: pfx("10.1.0.0/24"), Members: members("c", "d")},
	))
}

func TestPorts(t *testing.T) {
	req := base(Net{Name: "a", Members: members("x", "y")}, Net{Name: "b", Members: members("z")})
	req.Ports = map[string][]uint16{"x": {51820, 51821}}
	got := resolve(t, req)
	if !strings.Contains(got, "a 10.99.0.0/24 51822") || !strings.Contains(got, "b 10.99.1.0/24 51820") {
		t.Errorf("got %s", got)
	}

	req = base(Net{Name: "a", Port: 51820, Members: members("x", "y")})
	req.Ports = map[string][]uint16{"y": {51820}}
	wantErr(t, req, "port 51820 is already in use on y")

	wantErr(t, base(
		Net{Name: "a", Port: 4000, Members: members("x", "y")},
		Net{Name: "b", Port: 4000, Members: members("y", "z")},
	), "port 4000 is also used by network a")
}

func TestAddressErrors(t *testing.T) {
	wantErr(t, base(Net{Name: "n", Members: []Member{{Node: "a", Addr: spec(t, ".7")}, {Node: "b", Addr: spec(t, ".7")}}}),
		"members a and b both want 10.99.0.7")
	wantErr(t, base(Net{Name: "n", Members: []Member{{Node: "a", Addr: spec(t, ".2.7")}}}),
		"outside subnet 10.99.0.0/24")
	wantErr(t, base(Net{Name: "n", Subnet: pfx("10.0.0.0/30"), Members: members("a", "b", "c")}),
		"subnet 10.0.0.0/30 is full; no address left for c")
}

func TestPoolExhausted(t *testing.T) {
	req := base(Net{Name: "a", Members: members("x")}, Net{Name: "b", Members: members("y")})
	req.Pool = pfx("10.99.0.0/24")
	wantErr(t, req, "network b: no free /24 left in pool 10.99.0.0/24")
}

func TestUsable(t *testing.T) {
	p := pfx("10.0.0.0/30")
	for s, want := range map[string]bool{"10.0.0.0": false, "10.0.0.1": true, "10.0.0.2": true, "10.0.0.3": false, "10.0.0.4": false} {
		if usable(p, ip(s)) != want {
			t.Errorf("usable(%s) = %v", s, !want)
		}
	}
}
