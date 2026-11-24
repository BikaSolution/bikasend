package sign

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const usage = `usage:
  sign keygen -key FILE -pub FILE      make a key pair; reads the passphrase from stdin
  sign sums -out FILE FILE...           write "sha256  name" lines for the files
  sign sign -key FILE FILE              write FILE.sig; reads the passphrase from stdin
  sign verify -pub FILE FILE            check FILE.sig against the public key`

// Main runs the release signing tool. Passphrases come from stdin, never from flags or the
// environment, so they do not show in ps or reach child processes.
func Main(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	key := fs.String("key", "", "")
	pub := fs.String("pub", "", "")
	out := fs.String("out", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	files := fs.Args()
	switch {
	case args[0] == "keygen" && *key != "" && *pub != "" && len(files) == 0:
		return keygen(*key, *pub, stdin, stdout)
	case args[0] == "sums" && *out != "" && len(files) > 0:
		sums, err := Sums(files)
		if err != nil {
			return err
		}
		return writeNew(*out, sums, 0o644)
	case args[0] == "sign" && *key != "" && len(files) == 1:
		return signFile(*key, files[0], stdin)
	case args[0] == "verify" && *pub != "" && len(files) == 1:
		return verifyFile(*pub, files[0], stdout)
	}
	return errors.New(usage)
}

func keygen(keyPath, pubPath string, stdin io.Reader, stdout io.Writer) error {
	if old, err := os.ReadFile(pubPath); err == nil && strings.TrimSpace(string(old)) != "" { // #nosec G304 -- the maintainer names the file.
		return fmt.Errorf("%s already holds a public key; changing it locks out every installed app", pubPath)
	}
	pass, err := readPassphrase(stdin)
	if err != nil {
		return err
	}
	pubKey, file, err := NewKey(pass)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	if err := writeNew(keyPath, file, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, EncodePublicKey(pubKey), 0o644); err != nil { // #nosec G306 -- a public key.
		return err
	}
	_, err = fmt.Fprintf(stdout, "Wrote the private key to %s and the public key to %s.\n", keyPath, pubPath)
	return err
}

func signFile(keyPath, path string, stdin io.Reader) error {
	keyFile, err := os.ReadFile(keyPath) // #nosec G304 -- the maintainer names the key file.
	if err != nil {
		return err
	}
	pass, err := readPassphrase(stdin)
	if err != nil {
		return err
	}
	priv, err := OpenKey(keyFile, pass)
	if err != nil {
		return err
	}
	msg, err := os.ReadFile(path) // #nosec G304 -- the maintainer names the file to sign.
	if err != nil {
		return err
	}
	return writeNew(path+".sig", Sign(priv, msg), 0o644)
}

func verifyFile(pubPath, path string, stdout io.Writer) error {
	text, err := os.ReadFile(pubPath) // #nosec G304 -- the maintainer names the file.
	if err != nil {
		return err
	}
	pubKey, err := ParsePublicKey(text)
	if err != nil {
		return err
	}
	msg, err := os.ReadFile(path) // #nosec G304 -- the maintainer names the file.
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(path + ".sig") // #nosec G304 -- next to the file above.
	if err != nil {
		return err
	}
	if err := Verify(pubKey, msg, sig); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s: signature OK\n", filepath.Base(path))
	return err
}

func readPassphrase(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// writeNew never replaces a file: O_EXCL fails when the name exists.
func writeNew(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) // #nosec G304 -- the maintainer names the file.
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
