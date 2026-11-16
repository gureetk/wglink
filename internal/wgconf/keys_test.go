package wgconf

import (
	"encoding/base64"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
)

func TestPublicKeyMatchesRFC7748(t *testing.T) {
	priv, _ := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	pub, _ := hex.DecodeString("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")
	got, err := PublicKey(base64.StdEncoding.EncodeToString(priv))
	if err != nil || got != base64.StdEncoding.EncodeToString(pub) {
		t.Errorf("PublicKey = %s, %v", got, err)
	}
}

func TestGenerateKey(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil || !ValidKey(priv) || !ValidKey(pub) || priv == pub {
		t.Fatalf("GenerateKey = %s, %s, %v", priv, pub, err)
	}
	b, _ := base64.StdEncoding.DecodeString(priv)
	if b[0]&7 != 0 || b[31]&128 != 0 || b[31]&64 == 0 {
		t.Errorf("private key isn't clamped: %x", b)
	}
	if again, _ := PublicKey(priv); again != pub {
		t.Errorf("public key %s, derived again %s", pub, again)
	}
	// wg agrees, where it's installed.
	if _, err := exec.LookPath("wg"); err == nil {
		cmd := exec.Command("wg", "pubkey")
		cmd.Stdin = strings.NewReader(priv + "\n")
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != pub {
			t.Errorf("wg pubkey = %s, %v; want %s", out, err, pub)
		}
	}
}

func TestValidKey(t *testing.T) {
	for k, want := range map[string]bool{
		"pub0000000000000000000000000000000000000001=": true,
		"yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=": true,
		"yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk":  false,
		"q3F4sTqKG6c4kBxD7l6ZXpXwFfXoXJpPq2mWm3c1y;0=": false,
		"short=":    false,
		"not a key": false,
		"":          false,
		"yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk==": false,
	} {
		if ValidKey(k) != want {
			t.Errorf("ValidKey(%q) = %v", k, !want)
		}
	}
}

func TestWithPrivateKey(t *testing.T) {
	got := string(WithPrivateKey([]byte("# head\n[Interface]\nAddress = 10.99.0.4/24\n"), "KEY"))
	if got != "# head\n[Interface]\nPrivateKey = KEY\nAddress = 10.99.0.4/24\n" {
		t.Errorf("got %q", got)
	}
}

func TestPresharedKeys(t *testing.T) {
	a, err := GeneratePSK()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GeneratePSK()
	if !ValidKey(a) || a == b {
		t.Errorf("GeneratePSK: %q, %q", a, b)
	}
	// sha256 of "abc\n", as sha256sum prints it.
	if h := PSKHash("abc"); h != "edeaaff3f1774ad2888673770c6d64097e391bc362d7d6fb34982ddf0efd18cb" {
		t.Errorf("PSKHash = %s", h)
	}
	text := []byte("[Peer]\n# nas\nPresharedKey = @nas\n[Peer]\n# pi\nPresharedKey = @pi\n")
	got := string(WithPresharedKeys(text, map[string]string{"nas": a}))
	if got != "[Peer]\n# nas\nPresharedKey = "+a+"\n[Peer]\n# pi\nPresharedKey = @pi\n" {
		t.Errorf("WithPresharedKeys:\n%s", got)
	}
}
