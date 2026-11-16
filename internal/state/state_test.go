package state

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	st, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != 1 || len(st.Networks) != 0 {
		t.Errorf("state = %+v", st)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := New()
	lab := st.Net("lab")
	lab.Subnet = netip.MustParsePrefix("10.99.0.0/24")
	lab.Port = 51820
	pve := lab.Member("pve1")
	pve.IP = netip.MustParseAddr("10.99.0.1")
	pve.PubKey = "q3F4sTqKG6c4kBxD7l6ZXpXwFfXoXJpPq2mWm3c1yE0="
	pve.Applied = "abc"
	pve.PSKs = map[string]string{"nas": "def"}
	lab.Member("nas").IP = netip.MustParseAddr("10.99.0.10")
	st.Net("empty")
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	for _, want := range []string{`"subnet": "10.99.0.0/24"`, `"ip": "10.99.0.10"`, `"port": 51820`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("saved file lacks %s:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"empty": {`+"\n"+`      "subnet"`) {
		t.Errorf("zero subnet should be omitted:\n%s", raw)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if g := got.Networks["lab"].Members["pve1"]; !reflect.DeepEqual(g, pve) {
		t.Errorf("pve1 = %+v, want %+v", g, pve)
	}
	if got.Networks["empty"].Members == nil {
		t.Error("members map should be non-nil after load")
	}
	if name, ok := got.Networks["lab"].NodeName(pve.PubKey); !ok || name != "pve1" {
		t.Errorf("NodeName = %q, %v", name, ok)
	}
	if strings.Join(got.Networks["lab"].MemberNames(), ",") != "nas,pve1" {
		t.Errorf("MemberNames = %v", got.Networks["lab"].MemberNames())
	}
	if a := got.Networks["lab"].Addrs(); len(a) != 2 || a["nas"].String() != "10.99.0.10" {
		t.Errorf("Addrs = %v", a)
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	for name, content := range map[string]string{
		"version": `{"version": 2, "networks": {}}`,
		"unknown": `{"version": 1, "networks": {}, "secrets": 1}`,
		"syntax":  `{"version": 1,`,
		"addr":    `{"version": 1, "networks": {"n": {"members": {"a": {"ip": "nope"}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Error("Load succeeded")
			}
		})
	}
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); err == nil || !strings.Contains(err.Error(), "another wglink") {
		t.Errorf("second Lock err = %v", err)
	}
	unlock()
	unlock2, err := Lock(dir)
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	unlock2()
}
