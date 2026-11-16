package engine

import (
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/state"
)

func TestRotate(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(labNet, Options{})
	st, _ := state.Load(l.dir)
	old := st.Networks["lab"].Members["pve1"].PubKey

	_, p := l.plan(labNet, Options{Rotate: []string{"pve1"}})
	m := members(p, "lab")
	if !m["pve1"].Rotate || m["pve1"].Action != Live || strings.Join(m["pve1"].Reasons, ",") != "rotate its key" {
		t.Errorf("pve1 = %+v", m["pve1"])
	}
	if m["nas"].Action != Live || !strings.Contains(strings.Join(m["nas"].Reasons, ","), "peer pve1: new key") {
		t.Errorf("nas = %+v", m["nas"])
	}
	log := l.apply(labNet, Options{Rotate: []string{"pve1"}})
	if !strings.Contains(strings.Join(log, "\n"), "pve1: made a new key for wg-lab") {
		t.Errorf("log:\n%s", strings.Join(log, "\n"))
	}
	st, _ = state.Load(l.dir)
	pub := st.Networks["lab"].Members["pve1"].PubKey
	if pub == old || !strings.Contains(l.conf("nas", "wg-lab"), "PublicKey = "+pub) {
		t.Errorf("nas didn't get pve1's new key %s:\n%s", pub, l.conf("nas", "wg-lab"))
	}
	if !strings.Contains(l.conf("pve1", "wg-lab"), "PrivateKey = priv"+strings.TrimPrefix(pub, "pub0")) {
		t.Errorf("pve1's config lacks its new private key:\n%s", l.conf("pve1", "wg-lab"))
	}
	if _, p := l.plan(labNet, Options{}); !p.Empty() {
		t.Errorf("plan after rotating:\n%s", render(p))
	}
}

func TestStaleKeyIsLoaded(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(labNet, Options{})
	// A rotation that stopped after the new key was made.
	nd, _ := l.session(labNet).Node("pve1")
	if _, err := nd.RotateKey(ctx, "wg-lab"); err != nil {
		t.Fatal(err)
	}
	_, p := l.plan(labNet, Options{})
	m := members(p, "lab")
	if m["pve1"].Action != Live || strings.Join(m["pve1"].Reasons, ",") != "load its new key" || m["nas"].Action != Live {
		t.Errorf("plan:\n%s", render(p))
	}
	l.apply(labNet, Options{})
	if _, p := l.plan(labNet, Options{}); !p.Empty() {
		t.Errorf("plan after loading the key:\n%s", render(p))
	}
}

func TestRotateErrors(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	yaml := withPhone(phoneKey, "pve1, nas, phone")
	for _, c := range []struct{ node, want string }{
		{"phone", "phone is a manual node"},
		{"pi", "pi isn't in lab"},
	} {
		if _, err := l.session(yaml).Plan(ctx, Options{Networks: []string{"lab"}, Rotate: []string{c.node}}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("rotate %s: %v", c.node, err)
		}
	}
}
