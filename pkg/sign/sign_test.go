package sign

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	iterations = 100_000
	os.Exit(m.Run())
}

func TestKeys(t *testing.T) {
	const pass = "correct horse battery"
	pub, file, err := NewKey(pass)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file, []byte(pass)) {
		t.Fatal("the key file holds the passphrase")
	}
	priv, err := OpenKey(file, pass)
	if err != nil || !bytes.Equal(priv.Public().(ed25519.PublicKey), pub) {
		t.Fatalf("open = %v", err)
	}
	if _, err := OpenKey(file, pass+"!"); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	if _, _, err := NewKey("short"); !errors.Is(err, ErrFormat) {
		t.Errorf("short passphrase: %v", err)
	}
	damaged := bytes.Replace(file, []byte(`"box": "`), []byte(`"box": "AAAA`), 1)
	if _, err := OpenKey(damaged, pass); err == nil {
		t.Error("a damaged key file must not open")
	}
	for _, bad := range []string{`{}`, `not json`, `{"version":1,"kdf":"pbkdf2-sha256","iterations":1}`} {
		if _, err := OpenKey([]byte(bad), pass); !errors.Is(err, ErrFormat) {
			t.Errorf("OpenKey(%s) = %v", bad, err)
		}
	}

	got, err := ParsePublicKey(EncodePublicKey(pub))
	if err != nil || !bytes.Equal(got, pub) {
		t.Errorf("public key round trip: %v", err)
	}
	if k, err := ParsePublicKey([]byte(" \n")); k != nil || err != nil {
		t.Errorf("an empty public key file means no key: %v %v", k, err)
	}
	if _, err := ParsePublicKey([]byte("AAAA")); !errors.Is(err, ErrFormat) {
		t.Errorf("short public key: %v", err)
	}
}

func TestSignVerify(t *testing.T) {
	_, file, _ := NewKey("correct horse battery")
	priv, _ := OpenKey(file, "correct horse battery")
	pub := priv.Public().(ed25519.PublicKey)
	otherPub, _, _ := NewKey("another long passphrase")
	msg := []byte("abc  Bikasend-v1.0.0-linux-amd64.zip\n")
	sig := Sign(priv, msg)

	tests := []struct {
		name string
		pub  ed25519.PublicKey
		msg  []byte
		sig  []byte
		want error
	}{
		{name: "good", pub: pub, msg: msg, sig: sig},
		{name: "changed message", pub: pub, msg: append([]byte("x"), msg...), sig: sig, want: ErrBadSignature},
		{name: "other key", pub: otherPub, msg: msg, sig: sig, want: ErrBadSignature},
		{name: "no key", pub: nil, msg: msg, sig: sig, want: ErrFormat},
		{name: "not base64", pub: pub, msg: msg, sig: []byte("!!"), want: ErrFormat},
		{name: "short signature", pub: pub, msg: msg, sig: []byte("AAAA"), want: ErrFormat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Verify(tt.pub, tt.msg, tt.sig); !errors.Is(err, tt.want) {
				t.Errorf("Verify = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSums(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, n := range []string{"Bikasend-v1.0.0-windows-amd64.zip", "Bikasend-v1.0.0-linux-amd64.zip"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	sums, err := Sums(paths)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "linux-amd64.zip") {
		t.Fatalf("sums not sorted by name:\n%s", sums)
	}
	parsed, err := ParseSums(sums)
	if err != nil || len(parsed) != 2 || len(parsed["Bikasend-v1.0.0-linux-amd64.zip"]) != 64 {
		t.Errorf("parse = %v, %v", parsed, err)
	}

	bad := filepath.Join(dir, "two words.zip")
	if err := os.WriteFile(bad, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Sums([]string{bad}); !errors.Is(err, ErrFormat) {
		t.Errorf("a name with a space: %v", err)
	}

	h := strings.Repeat("a", 64)
	var many strings.Builder
	for i := range maxSumLines + 1 {
		fmt.Fprintf(&many, "%s  f%d\n", h, i)
	}
	for name, data := range map[string]string{
		"listed twice":   h + "  a.zip\n" + h + "  a.zip\n",
		"a path":         h + "  ../a.zip\n",
		"one space":      h + " a.zip\n",
		"upper-case hex": strings.ToUpper(h) + "  a.zip\n",
		"too many lines": many.String(),
		"too big":        strings.Repeat("x", maxSums+1),
	} {
		if _, err := ParseSums([]byte(data)); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
