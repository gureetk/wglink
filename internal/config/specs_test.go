package config

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseSubnet(t *testing.T) {
	for _, tc := range []struct {
		in, want, err string
	}{
		{in: "", want: "auto"},
		{in: "auto", want: "auto"},
		{in: "10.99.0.0/24", want: "10.99.0.0/24"},
		{in: "172.30.0.0/16", want: "172.30.0.0/16"},
		{in: "10.99.0.5/24", err: "did you mean 10.99.0.0/24"},
		{in: "fd00::/64", err: "must be IPv4"},
		{in: "10.0.0.0/31", err: "between /8 and /30"},
		{in: "10.0.0.0/7", err: "between /8 and /30"},
		{in: "ten", err: "not a CIDR"},
	} {
		s, err := ParseSubnet(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("ParseSubnet(%q) err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSubnet(%q): %v", tc.in, err)
			continue
		}
		if got, _ := s.MarshalText(); string(got) != tc.want {
			t.Errorf("ParseSubnet(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestAddrSpec(t *testing.T) {
	for _, tc := range []struct {
		spec, subnet, want, err string
	}{
		{spec: ".10", subnet: "10.99.0.0/24", want: "10.99.0.10"},
		{spec: ".5.10", subnet: "172.30.0.0/16", want: "172.30.5.10"},
		{spec: ".10", subnet: "172.30.0.0/16", want: "172.30.0.10"},
		{spec: "10.99.0.20", subnet: "10.99.0.0/24", want: "10.99.0.20"},
		{spec: ".0", subnet: "10.99.0.0/24", err: "network or broadcast"},
		{spec: ".255", subnet: "10.99.0.0/24", err: "network or broadcast"},
		{spec: ".5.10", subnet: "10.99.0.0/24", err: "outside subnet"},
		{spec: "10.99.1.1", subnet: "10.99.0.0/24", err: "outside subnet"},
		{spec: ".9", subnet: "10.99.0.0/29", err: "outside subnet"},
		{spec: "auto", subnet: "10.99.0.0/24", err: "automatic"},
	} {
		a, err := ParseAddrSpec(tc.spec)
		if err != nil {
			t.Fatalf("ParseAddrSpec(%q): %v", tc.spec, err)
		}
		got, err := a.In(netip.MustParsePrefix(tc.subnet))
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s in %s: err = %v, want %q", tc.spec, tc.subnet, err, tc.err)
			}
			continue
		}
		if err != nil || got.String() != tc.want {
			t.Errorf("%s in %s = %v, %v; want %s", tc.spec, tc.subnet, got, err, tc.want)
		}
	}
}

func TestParseAddrSpecErrors(t *testing.T) {
	for _, in := range []string{".256", ".01", ".1.2.3", ".", "..1", "fd00::1", "nas", ".-1"} {
		if _, err := ParseAddrSpec(in); err == nil {
			t.Errorf("ParseAddrSpec(%q) succeeded, want an error", in)
		}
	}
	for _, in := range []string{"auto", ".7", ".1.2", "10.0.0.1"} {
		a, err := ParseAddrSpec(in)
		if err != nil {
			t.Errorf("ParseAddrSpec(%q): %v", in, err)
		} else if a.String() != in {
			t.Errorf("ParseAddrSpec(%q).String() = %q", in, a.String())
		}
	}
}

func TestParseEndpoint(t *testing.T) {
	for _, tc := range []struct {
		in        string
		host      string
		port      uint16
		none      bool
		dial      string // with network port 51820
		err       string
		roundtrip bool
	}{
		{in: "nas.lan", host: "nas.lan", dial: "nas.lan:51820", roundtrip: true},
		{in: "nas.lan:51900", host: "nas.lan", port: 51900, dial: "nas.lan:51900", roundtrip: true},
		{in: "192.168.1.20", host: "192.168.1.20", dial: "192.168.1.20:51820", roundtrip: true},
		{in: "fd00::1", host: "fd00::1", dial: "[fd00::1]:51820"},
		{in: "[fd00::1]", host: "fd00::1", dial: "[fd00::1]:51820", roundtrip: true},
		{in: "[fd00::1]:4500", host: "fd00::1", port: 4500, dial: "[fd00::1]:4500", roundtrip: true},
		{in: "none", none: true, roundtrip: true},
		{in: "-oProxyCommand=x", err: "not a hostname"},
		{in: "nas.lan:0", err: "port must be"},
		{in: "nas.lan:", err: "port must be"},
		{in: "nas.lan:99999", err: "port must be"},
		{in: "[fd00::1]x", err: "unexpected"},
		{in: "[1.2.3.4]", err: "need an IPv6"},
		{in: "two words", err: "not a hostname"},
		{in: "", err: "not a hostname"},
	} {
		e, err := ParseEndpoint(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("ParseEndpoint(%q) err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseEndpoint(%q): %v", tc.in, err)
			continue
		}
		if e.Host != tc.host || e.Port != tc.port || e.None != tc.none {
			t.Errorf("ParseEndpoint(%q) = %+v", tc.in, e)
		}
		if !tc.none && e.Dial(51820) != tc.dial {
			t.Errorf("ParseEndpoint(%q).Dial = %q, want %q", tc.in, e.Dial(51820), tc.dial)
		}
		if tc.roundtrip && e.String() != tc.in {
			t.Errorf("ParseEndpoint(%q).String() = %q", tc.in, e.String())
		}
	}
}

func TestLastAddr(t *testing.T) {
	for p, want := range map[string]string{
		"10.99.0.0/24":  "10.99.0.255",
		"172.30.0.0/16": "172.30.255.255",
		"10.0.0.8/30":   "10.0.0.11",
	} {
		if got := lastAddr(netip.MustParsePrefix(p)); got.String() != want {
			t.Errorf("lastAddr(%s) = %s, want %s", p, got, want)
		}
	}
}
