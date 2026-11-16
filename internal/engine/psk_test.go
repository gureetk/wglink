package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/state"
	"github.com/gureetk/wglink/internal/wgconf"
)

const pskNet = base + "  lab: {psk: true, members: [pve1, nas, pi]}\n"

// psk reads the preshared key a fake node stored for peer.
func (l *lab) psk(nodeName, peer string) string {
	b, _ := os.ReadFile(filepath.Join(l.fakes[nodeName].Dir, "wg-lab.psk."+peer))
	return strings.TrimSpace(string(b))
}

func reasons(m *MemberPlan) string { return strings.Join(m.Reasons, "; ") }

func TestPresharedKeys(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	_, p := l.plan(pskNet, Options{})
	if r := reasons(members(p, "lab")["pve1"]); !strings.Contains(r, "new preshared keys with nas and pi") {
		t.Errorf("pve1: %s", r)
	}
	log := strings.Join(l.apply(pskNet, Options{}), "\n")
	if !strings.Contains(log, "pve1: stored 2 new preshared keys for wg-lab") {
		t.Errorf("log:\n%s", log)
	}
	pairs := [][2]string{{"pve1", "nas"}, {"pve1", "pi"}, {"nas", "pi"}}
	seen := map[string]bool{}
	for _, pr := range pairs {
		a, b := pr[0], pr[1]
		key := l.psk(a, b)
		if !wgconf.ValidKey(key) || l.psk(b, a) != key || seen[key] {
			t.Errorf("%s and %s: %q and %q", a, b, key, l.psk(b, a))
		}
		seen[key] = true
		if !strings.Contains(l.conf(a, "wg-lab"), "PresharedKey = "+key+"\n") {
			t.Errorf("%s's config lacks its key for %s:\n%s", a, b, l.conf(a, "wg-lab"))
		}
	}
	if _, p := l.plan(pskNet, Options{}); !p.Empty() {
		t.Fatalf("plan after apply:\n%s", render(p))
	}

	// Rotating a member's key replaces its preshared keys too.
	nasPi := l.psk("nas", "pi")
	old := l.psk("pve1", "nas")
	_, p = l.plan(pskNet, Options{Rotate: []string{"pve1"}})
	if r := reasons(members(p, "lab")["nas"]); !strings.Contains(r, "new preshared key with pve1") {
		t.Errorf("nas: %s", r)
	}
	l.apply(pskNet, Options{Rotate: []string{"pve1"}})
	if l.psk("pve1", "nas") == old || l.psk("nas", "pve1") != l.psk("pve1", "nas") || l.psk("nas", "pi") != nasPi {
		t.Error("rotating pve1 didn't replace just its preshared keys")
	}

	// A key stored on both sides but not loaded yet, as when apply stops early.
	s := l.session(pskNet)
	key, _ := wgconf.GeneratePSK()
	for _, pr := range [][2]string{{"nas", "pi"}, {"pi", "nas"}} {
		nd, _ := s.Node(pr[0])
		if err := nd.SetPSKs(ctx, "wg-lab", map[string]string{pr[1]: key}); err != nil {
			t.Fatal(err)
		}
	}
	_, p = l.plan(pskNet, Options{})
	m := members(p, "lab")
	if m["nas"].Action != Live || reasons(m["nas"]) != "load the preshared key for pi" || m["pve1"].Action != Keep {
		t.Errorf("plan:\n%s", render(p))
	}
	l.apply(pskNet, Options{})
	if !strings.Contains(l.conf("pi", "wg-lab"), "PresharedKey = "+key+"\n") {
		t.Errorf("pi didn't load the stored key:\n%s", l.conf("pi", "wg-lab"))
	}

	// Keys that differ get replaced.
	nd, _ := s.Node("pi")
	other, _ := wgconf.GeneratePSK()
	nd.SetPSKs(ctx, "wg-lab", map[string]string{"pve1": other})
	_, p = l.plan(pskNet, Options{})
	if r := reasons(members(p, "lab")["pve1"]); r != "new preshared key with pi" {
		t.Errorf("pve1: %s", r)
	}
	l.apply(pskNet, Options{})
	if k := l.psk("pi", "pve1"); k == other || k != l.psk("pve1", "pi") {
		t.Error("mismatched keys weren't replaced")
	}

	// Turning them off removes them.
	off := strings.Replace(pskNet, "psk: true, ", "", 1)
	l.apply(off, Options{})
	for _, n := range []string{"pve1", "nas", "pi"} {
		files, _ := filepath.Glob(filepath.Join(l.fakes[n].Dir, "*.psk.*"))
		if len(files) > 0 || strings.Contains(l.conf(n, "wg-lab"), "PresharedKey") {
			t.Errorf("%s keeps preshared keys: %v\n%s", n, files, l.conf(n, "wg-lab"))
		}
	}
}

func TestPresharedKeysWaitForBothSides(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(pskNet, Options{Nodes: []string{"pve1"}})
	if c := l.conf("pve1", "wg-lab"); strings.Contains(c, "PresharedKey") {
		t.Errorf("pve1 has keys its peers don't:\n%s", c)
	}
	l.apply(pskNet, Options{})
	if !strings.Contains(l.conf("pve1", "wg-lab"), "PresharedKey = "+l.psk("nas", "pve1")) {
		t.Errorf("pve1:\n%s", l.conf("pve1", "wg-lab"))
	}
	// One side changing alone keeps the key it has.
	l.apply(pskNet, Options{Nodes: []string{"nas"}, Rotate: []string{"nas"}})
	if l.psk("nas", "pve1") != l.psk("pve1", "nas") {
		t.Error("nas got a preshared key pve1 doesn't have")
	}
}

func TestPresharedKeysForManualNode(t *testing.T) {
	l := newLab(t, "pve1", "nas")
	yaml := strings.Replace(withPhone(phoneKey, "pve1, nas, phone"), "lab: {", "lab: {psk: true, ", 1)
	l.apply(yaml, Options{})
	if c := l.conf("pve1", "wg-lab"); strings.Contains(c, "# phone\nPublicKey = "+phoneKey+"\nPresharedKey") {
		t.Errorf("pve1 has a preshared key for phone before its export:\n%s", c)
	}

	ex, err := l.session(yaml).Export(ctx, "lab", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(func(yield func(string) bool) {
		for k := range ex.PSKs {
			if !yield(k) {
				return
			}
		}
	}); strings.Join(got, ",") != "nas,pve1" || len(ex.NoPSK) > 0 {
		t.Fatalf("PSKs for %v, none for %v", got, ex.NoPSK)
	}
	for _, peer := range []string{"pve1", "nas"} {
		if l.psk(peer, "phone") != ex.PSKs[peer] || !strings.Contains(string(ex.Config), "PresharedKey = @"+peer+"\n") {
			t.Errorf("%s: stored %q, gave %q\n%s", peer, l.psk(peer, "phone"), ex.PSKs[peer], ex.Config)
		}
	}
	full := string(wgconf.WithPresharedKeys(ex.Config, ex.PSKs))
	if !strings.Contains(full, "PresharedKey = "+ex.PSKs["nas"]+"\n") || strings.Contains(full, "@") {
		t.Errorf("filled in:\n%s", full)
	}
	st, _ := state.Load(l.dir)
	if h := st.Networks["lab"].Members["phone"].PSKs; h["pve1"] != wgconf.PSKHash(ex.PSKs["pve1"]) || len(h) != 2 {
		t.Errorf("recorded %v", h)
	}

	_, p := l.plan(yaml, Options{})
	m := members(p, "lab")
	if m["pve1"].Action != Live || !strings.Contains(reasons(m["pve1"]), "peer phone: preshared key on") || m["phone"].Action != Keep {
		t.Errorf("plan:\n%s", render(p))
	}
	l.apply(yaml, Options{})
	if !strings.Contains(l.conf("pve1", "wg-lab"), "PresharedKey = "+ex.PSKs["pve1"]+"\n") {
		t.Errorf("pve1:\n%s", l.conf("pve1", "wg-lab"))
	}
	if _, p := l.plan(yaml, Options{}); !p.Empty() || p.Exports() != 0 {
		t.Fatalf("plan after apply:\n%s", render(p))
	}

	// State rebuilt from the nodes keeps the keys the phone was given.
	os.Remove(filepath.Join(l.dir, state.FileName))
	if _, err := l.session(yaml).Rebuild(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, p := l.plan(yaml, Options{}); !p.Empty() || p.Exports() != 0 {
		t.Errorf("plan after rebuilding state:\n%s", render(p))
	}

	// A peer whose key changed needs the phone exported again; until then
	// it drops the key, matching neither side.
	nd, _ := l.session(yaml).Node("pve1")
	other, _ := wgconf.GeneratePSK()
	nd.SetPSKs(ctx, "wg-lab", map[string]string{"phone": other})
	_, p = l.plan(yaml, Options{})
	m = members(p, "lab")
	if m["phone"].Action != Export || !strings.HasPrefix(reasons(m["phone"]), "preshared key changed on pve1") {
		t.Errorf("phone = %+v", m["phone"])
	}

	// Peers it can't reach get no key.
	l.fakes["nas"].Fail("ssh: connect to host nas: No route to host")
	ex, err = l.session(yaml).Export(ctx, "lab", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ex.NoPSK, ",") != "nas" || len(ex.PSKs) != 1 || strings.Contains(string(ex.Config), "@nas") {
		t.Errorf("PSKs %v, none for %v:\n%s", ex.PSKs, ex.NoPSK, ex.Config)
	}
}
