package wgconf

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
)

// ValidKey reports whether s is a base64 WireGuard key.
func ValidKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32 && len(s) == 44
}

// GenerateKey makes a key pair the way wg genkey and wg pubkey do. wglink
// only does this for manual nodes, which it can't reach to do it there.
func GenerateKey() (private, public string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	b[0] &= 248
	b[31] = b[31]&127 | 64
	private = base64.StdEncoding.EncodeToString(b[:])
	public, err = PublicKey(private)
	return private, public, err
}

// PublicKey derives the public key for a base64 private key.
func PublicKey(private string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(private)
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// WithPrivateKey adds a PrivateKey line after [Interface], as nodes do when
// they write a config.
func WithPrivateKey(text []byte, key string) []byte {
	head, rest, ok := bytes.Cut(text, []byte("[Interface]\n"))
	if !ok {
		return text
	}
	var b bytes.Buffer
	b.Write(head)
	b.WriteString("[Interface]\nPrivateKey = " + key + "\n")
	b.Write(rest)
	return b.Bytes()
}
