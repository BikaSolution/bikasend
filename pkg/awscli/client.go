// Package awscli reads AWS secrets and credentials by running the user's own aws CLI.
// It adds no Go dependencies and reuses the CLI's SSO, profile and MFA support.
package awscli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/BikaSolution/bikasend/pkg/sigv4"
)

const (
	callTimeout   = 30 * time.Second
	signInTimeout = 5 * time.Minute
	maxOutput     = 8 << 20
	secretBatch   = 20
	paramBatch    = 10
)

// Every value starts with a letter or digit, so none can be read as a CLI option.
var (
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_+=.@:-]*$`)
	profilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$`)
	regionPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

type Client struct {
	bin string
}

// New uses bin when set, else the first aws found on PATH or in the usual install locations.
// GUI apps on macOS do not get the shell PATH, so the fallback list matters.
func New(bin string) *Client {
	if bin != "" {
		return &Client{bin: bin}
	}
	if p, err := exec.LookPath("aws"); err == nil {
		return &Client{bin: p}
	}
	candidates := []string{"/opt/homebrew/bin/aws", "/usr/local/bin/aws", "/usr/bin/aws", "/snap/bin/aws"}
	if runtime.GOOS == "windows" {
		candidates = []string{filepath.Join(os.Getenv("ProgramFiles"), "Amazon", "AWSCLIV2", "aws.exe")}
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() { // #nosec G703 -- fixed list of install locations, not user input.
			return &Client{bin: c}
		}
	}
	return &Client{}
}

func validID(id string) bool {
	return len(id) <= 2048 && idPattern.MatchString(id)
}

func (c *Client) Path() string {
	return c.bin
}

// Identity returns the AWS account of the profile; it fails with ErrNotSignedIn when the login expired.
func (c *Client) Identity(ctx context.Context, profile string) (string, error) {
	out, err := c.run(ctx, callTimeout, profile, "", "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		return "", err
	}
	var id struct{ Account string }
	if err := json.Unmarshal(out, &id); err != nil {
		return "", errors.Join(ErrOutput, err)
	}
	return id.Account, nil
}

// SignIn runs "aws sso login", which opens the browser and waits for the user.
func (c *Client) SignIn(ctx context.Context, profile string) error {
	_, err := c.run(ctx, signInTimeout, profile, "", "sso", "login")
	return err
}

// Secrets returns the SecretString of each ID. IDs that fail are listed in the error.
func (c *Client) Secrets(ctx context.Context, profile, region string, ids []string) (map[string]string, error) {
	for _, id := range ids {
		if !validID(id) {
			return nil, fmt.Errorf("%w: secret id %q", ErrInvalidInput, id)
		}
	}
	values := map[string]string{}
	var failed []error
	for start := 0; start < len(ids); start += secretBatch {
		batch := ids[start:min(start+secretBatch, len(ids))]
		args := append([]string{"secretsmanager", "batch-get-secret-value", "--output", "json", "--secret-id-list"}, batch...)
		out, err := c.run(ctx, callTimeout, profile, region, args...)
		if errors.Is(err, ErrAccessDenied) {
			// Some policies allow GetSecretValue but not BatchGetSecretValue.
			for _, id := range batch {
				v, err := c.secret(ctx, profile, region, id)
				if err != nil {
					failed = append(failed, err)
					continue
				}
				values[id] = v
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		var res struct {
			SecretValues []struct{ ARN, Name, SecretString string }
			Errors       []struct{ SecretId, ErrorCode, Message string }
		}
		if err := json.Unmarshal(out, &res); err != nil {
			return nil, errors.Join(ErrOutput, err)
		}
		for _, id := range batch {
			found := false
			for _, v := range res.SecretValues {
				if v.Name == id || v.ARN == id {
					values[id], found = v.SecretString, true
					break
				}
			}
			if !found {
				failed = append(failed, fmt.Errorf("%w: secret %q", ErrNotFound, id))
			}
		}
	}
	return values, errors.Join(failed...)
}

func (c *Client) secret(ctx context.Context, profile, region, id string) (string, error) {
	out, err := c.run(ctx, callTimeout, profile, region, "secretsmanager", "get-secret-value", "--output", "json", "--secret-id", id)
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", id, err)
	}
	var res struct{ SecretString *string }
	if err := json.Unmarshal(out, &res); err != nil {
		return "", errors.Join(ErrOutput, err)
	}
	if res.SecretString == nil {
		return "", fmt.Errorf("%w: secret %q is binary", ErrInvalidInput, id)
	}
	return *res.SecretString, nil
}

// Parameters reads SSM Parameter Store values, decrypting SecureStrings.
func (c *Client) Parameters(ctx context.Context, profile, region string, names []string) (map[string]string, error) {
	for _, n := range names {
		if !validID(strings.TrimPrefix(n, "/")) {
			return nil, fmt.Errorf("%w: parameter %q", ErrInvalidInput, n)
		}
	}
	values := map[string]string{}
	var failed []error
	for start := 0; start < len(names); start += paramBatch {
		batch := names[start:min(start+paramBatch, len(names))]
		args := append([]string{"ssm", "get-parameters", "--with-decryption", "--output", "json", "--names"}, batch...)
		out, err := c.run(ctx, callTimeout, profile, region, args...)
		if err != nil {
			return nil, err
		}
		var res struct {
			Parameters        []struct{ Name, Value string }
			InvalidParameters []string
		}
		if err := json.Unmarshal(out, &res); err != nil {
			return nil, errors.Join(ErrOutput, err)
		}
		for _, p := range res.Parameters {
			values[p.Name] = p.Value
		}
		for _, n := range res.InvalidParameters {
			failed = append(failed, fmt.Errorf("%w: parameter %q", ErrNotFound, n))
		}
	}
	return values, errors.Join(failed...)
}

// Credentials exports the profile's temporary credentials for request signing.
func (c *Client) Credentials(ctx context.Context, profile string) (sigv4.Credentials, error) {
	out, err := c.run(ctx, callTimeout, profile, "", "configure", "export-credentials", "--format", "process")
	if err != nil {
		return sigv4.Credentials{}, err
	}
	var res struct {
		AccessKeyId, SecretAccessKey, SessionToken string
		Expiration                                 time.Time
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return sigv4.Credentials{}, errors.Join(ErrOutput, err)
	}
	return sigv4.Credentials{AccessKeyID: res.AccessKeyId, SecretAccessKey: res.SecretAccessKey, SessionToken: res.SessionToken, Expires: res.Expiration}, nil
}

func (c *Client) run(ctx context.Context, timeout time.Duration, profile, region string, args ...string) ([]byte, error) {
	if c.bin == "" {
		return nil, ErrNotInstalled
	}
	if profile != "" {
		if !profilePattern.MatchString(profile) {
			return nil, fmt.Errorf("%w: profile %q", ErrInvalidInput, profile)
		}
		args = append(args, "--profile", profile)
	}
	if region != "" {
		if !regionPattern.MatchString(region) {
			return nil, fmt.Errorf("%w: region %q", ErrInvalidInput, region)
		}
		args = append(args, "--region", region)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...) // #nosec G204 -- fixed binary, no shell, every value checked against a pattern above.
	cmd.Env = append(os.Environ(), "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off", "NO_COLOR=1")
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxOutput, 64<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(ErrTimeout, ctx.Err())
		}
		return nil, classify(stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

func classify(stderr string, runErr error) error {
	msg := strings.TrimSpace(stderr)
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if msg == "" {
		msg = runErr.Error()
	}
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "token has expired"), strings.Contains(lower, "expiredtoken"),
		strings.Contains(lower, "sso session"), strings.Contains(lower, "error loading sso token"),
		strings.Contains(lower, "unable to locate credentials"), strings.Contains(lower, "invalidclienttokenid"),
		strings.Contains(lower, "unrecognizedclientexception"), strings.Contains(lower, "aws sso login"):
		return fmt.Errorf("%w: %s", ErrNotSignedIn, msg)
	case strings.Contains(lower, "could not be found") && strings.Contains(lower, "profile"):
		return fmt.Errorf("%w: %s", ErrProfile, msg)
	case strings.Contains(lower, "accessdenied"), strings.Contains(lower, "not authorized"):
		return fmt.Errorf("%w: %s", ErrAccessDenied, msg)
	case strings.Contains(lower, "resourcenotfound"), strings.Contains(lower, "parameternotfound"):
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	}
	return fmt.Errorf("%w: %s", ErrCLI, msg)
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}
