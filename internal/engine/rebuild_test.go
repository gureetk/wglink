package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gureetk/wglink/internal/state"
)

func TestRebuild(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	yaml := withPhone(phoneKey, "pve1, nas, phone, pi") + "  home: {members: [nas, pi], subnet: 10.50.0.0/24, port: 51900}\n"
	l.apply(yaml, Options{})
	if _, err := l.session(yaml).Export(ctx, "lab", "phone"); err != nil {
		t.Fatal(err)
	}
	l.apply(yaml, Options{})
	path := filepath.Join(l.dir, state.FileName)
	want, _ := os.ReadFile(path)
	os.Remove(path)

	got, err := l.session(yaml).Rebuild(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt, _ := os.ReadFile(path); !sameJSON(t, rebuilt, want) {
		t.Errorf("rebuilt state:\n%s\nwant:\n%s", rebuilt, want)
	}
	if len(got) != 2 || got[1].Name != "lab" || got[1].Subnet.String() != "10.99.0.0/24" || got[1].Port != 51820 {
		t.Fatalf("report = %+v", got)
	}
	for _, m := range got[1].Members {
		from := map[string]string{"pve1": "its config", "nas": "its config", "pi": "its config", "phone": "its peers' configs (manual node)"}[m.Node]
		if m.From != from || !m.Addr.IsValid() {
			t.Errorf("%s: %+v, want from %q", m.Node, m, from)
		}
	}
	if _, p := l.plan(yaml, Options{}); !p.Empty() || p.Exports() != 0 {
		t.Errorf("plan after rebuild:\n%s", render(p))
	}
}

func TestRebuildFromPeers(t *testing.T) {
	l := newLab(t, "pve1", "nas", "pi")
	l.apply(labNet, Options{})
	st, _ := state.Load(l.dir)
	nasIP := st.Networks["lab"].Members["nas"].IP

	// nas lost its config, and the state file is gone too.
	os.Remove(filepath.Join(l.fakes["nas"].Dir, "wg-lab.conf"))
	os.Remove(filepath.Join(l.dir, state.FileName))
	got, err := l.session(labNet).Rebuild(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := got[0].Members[1]; m.Node != "nas" || m.Addr != nasIP || m.From != "its peers' configs (it has no wg-lab)" {
		t.Errorf("nas = %+v", m)
	}
	// Its address stays, so apply rebuilds nas without disturbing pve1.
	_, p := l.plan(labNet, Options{})
	if m := members(p, "lab"); m["nas"].Action != Create || m["nas"].Addr != nasIP || m["pve1"].Action != Keep {
		t.Errorf("plan:\n%s", render(p))
	}

	// With nobody set up, nothing is recorded.
	got, err = l.session(base+"  lab: {members: [pve1, nas]}\n  idle: {members: [pi, nas]}\n").Rebuild(ctx, []string{"idle"})
	if err != nil || got[0].Subnet.IsValid() {
		t.Errorf("idle: %+v, %v", got, err)
	}
	if st, _ := state.Load(l.dir); st.Networks["idle"] != nil || st.Networks["lab"] == nil {
		t.Errorf("state networks: %v", st.Networks)
	}
	if _, err := l.session(labNet).Rebuild(ctx, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), "no network named ghost") {
		t.Errorf("unknown network: %v", err)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}
