// Package sign signs and checks release checksum files with ed25519. The private key stays in a
// passphrase-encrypted file on the maintainer's machine; apps carry only the public key.
package sign

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	ErrBadPassphrase = errors.New("sign: wrong passphrase or damaged key file")
	ErrBadSignature  = errors.New("sign: signature does not match")
	ErrFormat        = errors.New("sign: bad format")
)

const (
	MinPassphrase = 12
	maxSums       = 64 << 10
	maxSumLines   = 100
)

// iterations is a variable so tests can make key files fast.
var iterations = 600_000

type keyFile struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Box        []byte `json:"box"`
}

// NewKey makes a key pair and returns the public key and the encrypted private key file.
func NewKey(passphrase string) (ed25519.PublicKey, []byte, error) {
	if len(passphrase) < MinPassphrase {
		return nil, nil, fmt.Errorf("%w: the passphrase needs at least %d characters", ErrFormat, MinPassphrase)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	kf := keyFile{Version: 1, KDF: "pbkdf2-sha256", Iterations: iterations, Salt: make([]byte, 16)}
	if _, err := rand.Read(kf.Salt); err != nil {
		return nil, nil, err
	}
	gcm, err := keyCipher(passphrase, kf.Salt, kf.Iterations)
	if err != nil {
		return nil, nil, err
	}
	kf.Nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(kf.Nonce); err != nil {
		return nil, nil, err
	}
	kf.Box = gcm.Seal(nil, kf.Nonce, priv.Seed(), nil)
	file, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return pub, append(file, '\n'), nil
}

// OpenKey decrypts a key file made by NewKey.
func OpenKey(file []byte, passphrase string) (ed25519.PrivateKey, error) {
	var kf keyFile
	if err := json.Unmarshal(file, &kf); err != nil {
		return nil, errors.Join(ErrFormat, err)
	}
	if kf.Version != 1 || kf.KDF != "pbkdf2-sha256" || kf.Iterations < 100_000 || kf.Iterations > 10_000_000 {
		return nil, fmt.Errorf("%w: not a Bikasend key file", ErrFormat)
	}
	gcm, err := keyCipher(passphrase, kf.Salt, kf.Iterations)
	if err != nil {
		return nil, err
	}
	if len(kf.Nonce) != gcm.NonceSize() {
		return nil, ErrBadPassphrase
	}
	seed, err := gcm.Open(nil, kf.Nonce, kf.Box, nil)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, ErrBadPassphrase
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func keyCipher(passphrase string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// EncodePublicKey and ParsePublicKey use one base64 line; an empty file means no key.
func EncodePublicKey(pub ed25519.PublicKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(pub) + "\n")
}

func ParsePublicKey(text []byte) (ed25519.PublicKey, error) {
	s := strings.TrimSpace(string(text))
	if s == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.Join(ErrFormat, err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key", ErrFormat)
	}
	return ed25519.PublicKey(b), nil
}

// Sign returns the signature file for msg: one base64 line.
func Sign(priv ed25519.PrivateKey, msg []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)) + "\n")
}

func Verify(pub ed25519.PublicKey, msg, sigFile []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: no public key", ErrFormat)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil {
		return errors.Join(ErrFormat, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature", ErrFormat)
	}
	if !ed25519.Verify(pub, msg, sig) {
		return ErrBadSignature
	}
	return nil
}

var sumLine = regexp.MustCompile(`^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]{0,120})$`)

// Sums returns "sha256  name" lines for the files, sorted by name, in sha256sum's format.
func Sums(paths []string) ([]byte, error) {
	lines := make([]string, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p) // #nosec G304 -- the maintainer names the release files to sum.
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		line := hex.EncodeToString(h.Sum(nil)) + "  " + filepath.Base(p)
		if !sumLine.MatchString(line) {
			return nil, fmt.Errorf("%w: file name %q", ErrFormat, filepath.Base(p))
		}
		lines = append(lines, line)
	}
	slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// ParseSums reads a checksum file into name → sha256. It refuses anything unexpected.
func ParseSums(data []byte) (map[string]string, error) {
	if len(data) > maxSums {
		return nil, fmt.Errorf("%w: checksum file over %d KB", ErrFormat, maxSums>>10)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if len(out) >= maxSumLines {
			return nil, fmt.Errorf("%w: more than %d checksums", ErrFormat, maxSumLines)
		}
		m := sumLine.FindStringSubmatch(sc.Text())
		if m == nil {
			return nil, fmt.Errorf("%w: checksum line", ErrFormat)
		}
		if _, dup := out[m[2]]; dup {
			return nil, fmt.Errorf("%w: %s listed twice", ErrFormat, m[2])
		}
		out[m[2]] = m[1]
	}
	if err := sc.Err(); err != nil {
		return nil, errors.Join(ErrFormat, err)
	}
	return out, nil
}
