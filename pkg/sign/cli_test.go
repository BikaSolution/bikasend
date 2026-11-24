package sign

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "keys", "release.key")
	pub := filepath.Join(dir, "release.pub")
	if err := os.WriteFile(pub, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := Main(args, strings.NewReader(stdin), &out)
		return out.String(), err
	}
	const pass = "correct horse battery\n"

	if _, err := run(pass, "keygen", "-key", key, "-pub", pub); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(key); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("key file mode: %v %v", fi, err)
	}
	if _, err := run(pass, "keygen", "-key", key+"2", "-pub", pub); err == nil {
		t.Error("keygen must not replace a public key that is already set")
	}
	if err := os.WriteFile(pub+"2", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(pass, "keygen", "-key", key, "-pub", pub+"2"); !errors.Is(err, os.ErrExist) {
		t.Errorf("keygen must not replace a key file: %v", err)
	}

	zip := filepath.Join(dir, "Bikasend-v1.0.0-linux-amd64.zip")
	if err := os.WriteFile(zip, []byte("app"), 0o600); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "Bikasend-v1.0.0-SHA256SUMS")
	if _, err := run("", "sums", "-out", sums, zip); err != nil {
		t.Fatal(err)
	}
	if _, err := run("wrong passphrase!\n", "sign", "-key", key, sums); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("a wrong passphrase must not sign: %v", err)
	}
	if _, err := run(pass, "sign", "-key", key, sums); err != nil {
		t.Fatal(err)
	}
	if _, err := run(pass, "sign", "-key", key, sums); !errors.Is(err, os.ErrExist) {
		t.Errorf("sign must not replace a signature: %v", err)
	}
	if out, err := run("", "verify", "-pub", pub, sums); err != nil || !strings.Contains(out, "signature OK") {
		t.Errorf("verify = %q, %v", out, err)
	}
	if err := os.WriteFile(sums, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("", "verify", "-pub", pub, sums); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a changed file must fail: %v", err)
	}

	for _, args := range [][]string{nil, {"nope"}, {"sign", sums}, {"sums", "-out", sums}, {"verify", "-pub", pub}} {
		if err := Main(args, strings.NewReader(""), io.Discard); err == nil {
			t.Errorf("Main(%q) must fail with usage", args)
		}
	}
}
