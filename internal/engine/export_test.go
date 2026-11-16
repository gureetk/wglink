package engine

import (
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

const phoneKey = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="

// withPhone is base plus a manual node, keyless unless key is set.
func withPhone(key, members string) string {
	return strings.Replace(base, "networks:\n", "  phone: {manual: {pubkey: '"+key+"'}, endpoint: none, keepalive: 25}\nnetworks:\n", 1) +
		"  lab: {members: [" + members + "]}\n"
}

func TestManualNode(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	keyless := withPhone("", "pve1, nas, phone")
	_, p := l.plan(keyless, Options{})
	m := members(p, "lab")
	if m["phone"].Action != Export || !strings.Contains(m["phone"].Reasons[0], `no key yet: run "wglink export lab phone"`) {
		t.Errorf("phone = %+v", m["phone"])
	}
	if out := render(p); !strings.Contains(out, "! phone  10.99.0.3") || !strings.Contains(out, "2 to create, 0 to change live, 0 to restart, 0 to remove. 1 manual config to export.") {
		t.Errorf("plan:\n%s", out)
	}
	l.apply(keyless, Options{})
	if strings.Contains(l.conf("pve1", "wg-lab"), "# phone") {
		t.Error("pve1 lists phone before it has a key")
	}

	// The CLI saves a key, then exports.
	keyed := withPhone(phoneKey, "pve1, nas, phone")
	ex, err := l.session(keyed).Export(ctx, "lab", "phone")
	if err != nil {
		t.Fatal(err)
	}
	conf := string(ex.Config)
	for _, want := range []string{
		"# Exported by wglink for phone (network lab).",
		"[Interface]\nAddress = 10.99.0.3/24\n\n[Peer]\n# nas\n",
		"Endpoint = 192.168.1.20:51820\nPersistentKeepalive = 25\n",
		"# pve1\nPublicKey = pub0000000000000000000000000000000000000101=\nAllowedIPs = 10.99.0.1/32\nEndpoint = 192.168.1.10:51820",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("config lacks %q:\n%s", want, conf)
		}
	}
	if len(ex.Missing) != 0 || ex.Plan.Empty() {
		t.Errorf("missing %v, plan empty %v", ex.Missing, ex.Plan.Empty())
	}
	st, _ := state.Load(l.dir)
	if sm := st.Networks["lab"].Members["phone"]; sm.IP.String() != "10.99.0.3" || sm.PubKey != phoneKey || sm.Applied != wgconf.Hash(ex.Config) {
		t.Errorf("state = %+v", sm)
	}

	// Its peers add it, and its export stays current.
	_, p = l.plan(keyed, Options{})
	if m := members(p, "lab"); m["phone"].Action != Keep || m["pve1"].Action != Live {
		t.Errorf("after export: phone %+v, pve1 %+v", m["phone"], m["pve1"])
	}
	l.apply(keyed, Options{})
	if c := l.conf("pve1", "wg-lab"); !strings.Contains(c, "# phone\nPublicKey = "+phoneKey+"\nAllowedIPs = 10.99.0.3/32\n") || strings.Contains(c, "Endpoint = none") {
		t.Errorf("pve1:\n%s", c)
	}
	if _, p = l.plan(keyed, Options{}); !p.Empty() || p.Exports() != 0 {
		t.Errorf("plan after apply:\n%s", render(p))
	}

	// A new member makes the export stale until it's done again.
	three := withPhone(phoneKey, "pve1, nas, phone, pi")
	l.apply(three, Options{})
	if _, p = l.plan(three, Options{}); members(p, "lab")["phone"].Action != Export || !strings.Contains(render(p), "its config changed") {
		t.Errorf("plan after pi joined:\n%s", render(p))
	}
	ex, err = l.session(three).Export(ctx, "lab", "phone")
	if err != nil || !strings.Contains(string(ex.Config), "# pi\n") {
		t.Fatalf("export with pi: %v\n%s", err, ex.Config)
	}
	if _, p = l.plan(three, Options{}); p.Exports() != 0 {
		t.Errorf("plan after export:\n%s", render(p))
	}

	// Status can only ask phone's peers.
	l.clock("pve1", 1700000100, 10)
	r := l.status(three, StatusOptions{NoPing: true})
	for _, k := range r.Networks[0].Links {
		if k.A == "pve1" && k.B == "phone" && (k.State != LinkOK || k.Note != "handshake 10s ago") {
			t.Errorf("pve1 <-> phone = %+v", k)
		}
	}
	for _, mr := range r.Networks[0].Members {
		if mr.Node == "phone" && mr.State != MemberManual {
			t.Errorf("phone = %+v", mr)
		}
	}
	if strings.Contains(l.fakes["pve1"].Log(), "phone") {
		t.Error("wglink tried to reach phone")
	}

	// Leaving drops it from its peers and from state.
	l.apply(base+"  lab: {members: [pve1, nas, pi]}\n", Options{})
	st, _ = state.Load(l.dir)
	if _, ok := st.Networks["lab"].Members["phone"]; ok || strings.Contains(l.conf("pve1", "wg-lab"), "# phone") {
		t.Error("phone is still in state or on pve1")
	}
}

func TestExportErrors(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	yaml := withPhone("", "pve1, nas")
	for _, c := range []struct{ network, node, want string }{
		{"ghost", "phone", "no network named ghost"},
		{"lab", "phone", "phone isn't in network lab"},
		{"lab", "nas", "nas isn't a manual node"},
	} {
		if _, err := l.session(yaml).Export(ctx, c.network, c.node); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("export %s %s: %v, want %q", c.network, c.node, err, c.want)
		}
	}
	if _, err := l.session(withPhone("", "pve1, phone")).Export(ctx, "lab", "phone"); err == nil || !strings.Contains(err.Error(), "no public key") {
		t.Errorf("keyless export: %v", err)
	}
}
