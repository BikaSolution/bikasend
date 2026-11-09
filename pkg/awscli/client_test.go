package awscli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHelperProcess is the fake aws CLI. The test binary runs itself with GO_WANT_HELPER_PROCESS=1.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	cmd := strings.Join(args, " ")
	scenario := os.Getenv("FAKE_AWS")
	switch {
	case scenario == "expired":
		fmt.Fprintln(os.Stderr, "Error when retrieving token from sso: Token has expired and refresh failed")
		os.Exit(255)
	case scenario == "denied-batch" && strings.Contains(cmd, "batch-get-secret-value"):
		fmt.Fprintln(os.Stderr, "An error occurred (AccessDeniedException) when calling the BatchGetSecretValue operation")
		os.Exit(254)
	case strings.HasPrefix(cmd, "sts get-caller-identity"):
		fmt.Print(`{"Account":"123456789012","Arn":"arn:aws:sts::123456789012:assumed-role/dev"}`)
	case strings.HasPrefix(cmd, "secretsmanager batch-get-secret-value"):
		if !strings.Contains(cmd, "--profile staging --region eu-west-1") {
			fmt.Fprintln(os.Stderr, "missing profile or region: "+cmd)
			os.Exit(1)
		}
		fmt.Print(`{"SecretValues":[{"Name":"app/keys","ARN":"arn:aws:secretsmanager:eu-west-1:1:secret:app/keys-AbC","SecretString":"{\"api\":\"k1\"}"}],"Errors":[{"SecretId":"app/missing","ErrorCode":"ResourceNotFoundException"}]}`)
	case strings.HasPrefix(cmd, "secretsmanager get-secret-value --output json --secret-id app/keys"):
		fmt.Print(`{"Name":"app/keys","SecretString":"plain"}`)
	case strings.HasPrefix(cmd, "ssm get-parameters"):
		fmt.Print(`{"Parameters":[{"Name":"/app/url","Value":"https://x"}],"InvalidParameters":["/app/nope"]}`)
	case strings.HasPrefix(cmd, "configure export-credentials"):
		fmt.Print(`{"Version":1,"AccessKeyId":"AK","SecretAccessKey":"SK","SessionToken":"TK","Expiration":"2030-01-01T00:00:00Z"}`)
	default:
		fmt.Fprintln(os.Stderr, "unexpected: "+cmd)
		os.Exit(2)
	}
	os.Exit(0)
}

// fakeClient points the client at a script that re-runs this test binary as the fake aws CLI.
func fakeClient(t *testing.T, scenario string) *Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake aws CLI is a shell script")
	}
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("FAKE_AWS", scenario)
	script := filepath.Join(t.TempDir(), "aws")
	body := "#!/bin/sh\nexec \"" + os.Args[0] + "\" -test.run=TestHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Client{bin: script}
}

func TestClient(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		scenario string
		call     func(c *Client) (any, error)
		want     any
		wantErr  error
	}{
		{
			name: "identity",
			call: func(c *Client) (any, error) { return c.Identity(ctx, "staging") },
			want: "123456789012",
		},
		{
			name:     "expired sso login",
			scenario: "expired",
			call:     func(c *Client) (any, error) { return c.Identity(ctx, "staging") },
			wantErr:  ErrNotSignedIn,
		},
		{
			name: "batch secrets with one missing",
			call: func(c *Client) (any, error) {
				v, err := c.Secrets(ctx, "staging", "eu-west-1", []string{"app/keys", "app/missing"})
				return fmt.Sprint(v), err
			},
			want:    `map[app/keys:{"api":"k1"}]`,
			wantErr: ErrNotFound,
		},
		{
			name:     "batch denied falls back to single reads",
			scenario: "denied-batch",
			call: func(c *Client) (any, error) {
				v, err := c.Secrets(ctx, "staging", "eu-west-1", []string{"app/keys"})
				return fmt.Sprint(v), err
			},
			want: "map[app/keys:plain]",
		},
		{
			name:    "secret id that looks like a flag is refused",
			call:    func(c *Client) (any, error) { return c.Secrets(ctx, "", "", []string{"--endpoint-url=http://evil"}) },
			wantErr: ErrInvalidInput,
		},
		{
			name:    "profile that looks like a flag is refused",
			call:    func(c *Client) (any, error) { return c.Identity(ctx, "-x") },
			wantErr: ErrInvalidInput,
		},
		{
			name: "parameters",
			call: func(c *Client) (any, error) {
				v, err := c.Parameters(ctx, "", "", []string{"/app/url", "/app/nope"})
				return fmt.Sprint(v), err
			},
			want:    "map[/app/url:https://x]",
			wantErr: ErrNotFound,
		},
		{
			name: "credentials",
			call: func(c *Client) (any, error) {
				cr, err := c.Credentials(ctx, "")
				return cr.AccessKeyID + "/" + cr.SessionToken, err
			},
			want: "AK/TK",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call(fakeClient(t, tt.scenario))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.want != nil && got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	if _, err := (&Client{}).Identity(ctx, ""); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("no binary: err = %v, want ErrNotInstalled", err)
	}
}
