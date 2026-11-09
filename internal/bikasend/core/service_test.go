package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BikaSolution/bikasend/pkg/awscli"
	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
	"github.com/BikaSolution/bikasend/pkg/sigv4"
)

type fakeAWS struct {
	signedIn bool
	secrets  map[string]string
	params   map[string]string
	calls    []string
}

func (f *fakeAWS) Path() string { return "/fake/aws" }

func (f *fakeAWS) Identity(_ context.Context, profile string) (string, error) {
	f.calls = append(f.calls, "identity "+profile)
	if !f.signedIn {
		return "", fmt.Errorf("%w: token expired", awscli.ErrNotSignedIn)
	}
	return "123456789012", nil
}

func (f *fakeAWS) SignIn(_ context.Context, profile string) error {
	f.calls = append(f.calls, "signin "+profile)
	f.signedIn = true
	return nil
}

func (f *fakeAWS) Secrets(_ context.Context, profile, region string, ids []string) (map[string]string, error) {
	f.calls = append(f.calls, "secrets "+profile+" "+region+" "+strings.Join(ids, ","))
	out := map[string]string{}
	var err error
	for _, id := range ids {
		if v, ok := f.secrets[id]; ok {
			out[id] = v
		} else {
			err = fmt.Errorf("%w: secret %q", awscli.ErrNotFound, id)
		}
	}
	return out, err
}

func (f *fakeAWS) Parameters(_ context.Context, _, _ string, names []string) (map[string]string, error) {
	out := map[string]string{}
	for _, n := range names {
		out[n] = f.params[n]
	}
	return out, nil
}

func (f *fakeAWS) Credentials(context.Context, string) (sigv4.Credentials, error) {
	return sigv4.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TK"}, nil
}

const testConfig = `{
  "variables": {"tenant": "demo", "itemId": "42"},
  "environments": {
    "local": {"variables": {"baseUrl": "BASE", "api_key": {"prompt": "Local API key"}}},
    "staging": {
      "hosts": ["127.0.0.1"],
      "aws": {"profile": "staging", "region": "eu-west-1"},
      "variables": {
        "baseUrl": "BASE",
        "api_key": {"aws": "app/keys", "key": "api"},
        "plain": {"aws": "app/plain"},
        "url": {"ssm": "/app/url"}
      }
    }
  }
}`

// newTestService opens a temp folder holding a swagger file and .bika.json, with base as BASE.
func newTestService(t *testing.T, base, config string, aws *fakeAWS) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "swagger.json"), []byte(swagger2), 0o600); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(root, configName), []byte(strings.ReplaceAll(config, "BASE", base)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if aws == nil {
		aws = &fakeAWS{}
	}
	s := NewService(Options{
		Version:   "v1.2.3",
		ConfigDir: t.TempDir(),
		AWS:       func(string) AWS { return aws },
		Client:    http.DefaultClient,
	})
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	// Windows cannot delete a folder with an open handle; close it before TempDir cleanup.
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.root != nil {
			_ = s.root.Close()
		}
	})
	return s, root
}

func findVar(w bikasend.Workspace, scope, name string) (bikasend.VariableView, bool) {
	for _, v := range w.Variables {
		if v.Scope == scope && v.Name == name {
			return v, true
		}
	}
	return bikasend.VariableView{}, false
}

func TestTrust(t *testing.T) {
	s, root := newTestService(t, "http://x", testConfig, nil)

	w := s.Workspace()
	if w.Trusted || len(w.Environments) != 0 || w.ConfigFile != configName {
		t.Fatalf("an untrusted .bika.json must be ignored: %+v", w)
	}
	if _, ok := findVar(w, bikasend.ScopeFolder, "tenant"); ok {
		t.Error("folder variables visible before trust")
	}
	if _, err := s.SetVariable(bikasend.ScopeFolder, "x", bikasend.VariableInput{Kind: bikasend.KindText, Value: "1"}); !errors.Is(err, bikasend.ErrInvalidConfig) {
		t.Errorf("editing an untrusted file: err = %v", err)
	}

	w, err := s.TrustFolder()
	if err != nil || !w.Trusted || len(w.Environments) != 2 || w.Environment != "local" {
		t.Fatalf("after trust: %+v, %v", w, err)
	}

	// A change made outside the app asks for trust again.
	if err := os.WriteFile(filepath.Join(root, configName), []byte(`{"variables":{"tenant":"evil"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.Changed() {
		t.Error("Changed() missed an edit to .bika.json")
	}
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	if s.Workspace().Trusted {
		t.Error("edited .bika.json kept its trust")
	}

	if err := os.WriteFile(filepath.Join(root, configName), make([]byte, maxConfig+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	if s.Workspace().ConfigError == "" {
		t.Error("an oversized .bika.json must be refused")
	}
}

func TestVariablesAndScopes(t *testing.T) {
	s, root := newTestService(t, "http://x", testConfig, nil)
	if _, err := s.TrustFolder(); err != nil {
		t.Fatal(err)
	}

	w := s.Workspace()
	if len(w.Prompts) != 1 || w.Prompts[0] != "api_key" {
		t.Errorf("prompts = %v", w.Prompts)
	}
	if _, err := s.SetPrompt("api_key", "local-secret"); err != nil {
		t.Fatal(err)
	}
	if v, _ := findVar(s.Workspace(), bikasend.ScopeEnvironment, "api_key"); !v.Resolved || !v.Secret || v.Value != "" {
		t.Errorf("prompt variable view leaks or is unresolved: %+v", v)
	}
	if got, err := s.Reveal("api_key"); err != nil || got != "local-secret" {
		t.Errorf("reveal = %q, %v", got, err)
	}

	tests := []struct {
		name    string
		scope   string
		varName string
		in      bikasend.VariableInput
		wantErr error
	}{
		{name: "session secret", scope: bikasend.ScopeSession, varName: "tenant", in: bikasend.VariableInput{Kind: bikasend.KindSecret, Value: "s"}},
		{name: "folder text", scope: bikasend.ScopeFolder, varName: "region", in: bikasend.VariableInput{Kind: bikasend.KindText, Value: "eu"}},
		{name: "environment aws ref", scope: bikasend.ScopeEnvironment, varName: "db_pass", in: bikasend.VariableInput{Kind: bikasend.KindAWS, Value: "app/db", Key: "password"}},
		{name: "secret in plain text refused", scope: bikasend.ScopeFolder, varName: "api_token", in: bikasend.VariableInput{Kind: bikasend.KindText, Value: "abc"}, wantErr: bikasend.ErrSecretInFile},
		{name: "bad name", scope: bikasend.ScopeSession, varName: "has space", in: bikasend.VariableInput{Kind: bikasend.KindText}, wantErr: bikasend.ErrInvalidVariable},
		{name: "aws without id", scope: bikasend.ScopeGlobal, varName: "x", in: bikasend.VariableInput{Kind: bikasend.KindAWS}, wantErr: bikasend.ErrInvalidVariable},
		{name: "global text", scope: bikasend.ScopeGlobal, varName: "me", in: bikasend.VariableInput{Kind: bikasend.KindText, Value: "nico"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.SetVariable(tt.scope, tt.varName, tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}

	w = s.Workspace()
	tenantSession, _ := findVar(w, bikasend.ScopeSession, "tenant")
	tenantFolder, _ := findVar(w, bikasend.ScopeFolder, "tenant")
	if !tenantSession.Active || tenantFolder.Active || tenantFolder.Shadows != bikasend.ScopeSession {
		t.Errorf("shadowing: session %+v folder %+v", tenantSession, tenantFolder)
	}
	data, _ := os.ReadFile(filepath.Join(root, configName))
	if !strings.Contains(string(data), `"db_pass": {`) || !strings.Contains(string(data), `"region": "eu"`) {
		t.Errorf(".bika.json not updated:\n%s", data)
	}
	if !s.Workspace().Trusted {
		t.Error("our own write must keep the trust")
	}

	if _, err := s.DeleteVariable(bikasend.ScopeFolder, "region"); err != nil {
		t.Fatal(err)
	}
	if _, ok := findVar(s.Workspace(), bikasend.ScopeFolder, "region"); ok {
		t.Error("delete did not remove the variable")
	}
	if _, err := s.SelectEnvironment("nope"); !errors.Is(err, bikasend.ErrUnknownEnv) {
		t.Errorf("unknown env: %v", err)
	}
}

func TestLoadSecrets(t *testing.T) {
	aws := &fakeAWS{secrets: map[string]string{"app/keys": `{"api":"k-123456"}`, "app/plain": "plain-secret"}, params: map[string]string{"/app/url": "https://x"}}
	s, _ := newTestService(t, "http://x", testConfig, aws)
	if _, err := s.TrustFolder(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectEnvironment("staging"); err != nil {
		t.Fatal(err)
	}

	w, err := s.LoadSecrets(context.Background())
	if !errors.Is(err, bikasend.ErrSecrets) || w.AWS.State != "signed-out" {
		t.Fatalf("signed out: state %q, err %v", w.AWS.State, err)
	}
	w, err = s.SignIn(context.Background())
	if err != nil || w.AWS.State != "loaded" || w.AWS.Count != 3 || w.AWS.Account != "123456789012" {
		t.Fatalf("after sign-in: %+v, %v", w.AWS, err)
	}
	if !strings.Contains(strings.Join(aws.calls, "|"), "secrets staging eu-west-1 app/keys,app/plain") {
		t.Errorf("calls = %v", aws.calls)
	}
	for name, want := range map[string]string{"api_key": "k-123456", "plain": "plain-secret", "url": "https://x"} {
		if got, err := s.Reveal(name); err != nil || got != want {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	for _, v := range w.Variables {
		if v.Secret && v.Value != "" {
			t.Errorf("secret %s leaked into the view", v.Name)
		}
	}

	w = s.ForgetSecrets()
	if w.AWS.State != "none" {
		t.Errorf("forget: %+v", w.AWS)
	}
	if _, err := s.Reveal("api_key"); !errors.Is(err, bikasend.ErrUndefinedVariable) {
		t.Errorf("secret still readable after forget: %v", err)
	}
}

func TestConfigCreatedOnFirstSaveAndSettings(t *testing.T) {
	s, root := newTestService(t, "", "", nil)
	if _, err := os.Stat(filepath.Join(root, configName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("opening a folder must not create %s: %v", configName, err)
	}
	w, err := s.SetVariable(bikasend.ScopeEnvironment, "tenant", bikasend.VariableInput{Kind: bikasend.KindText, Value: "demo"})
	if err != nil || !w.Trusted || w.Environment != "local" || w.ConfigFile != configName {
		t.Fatalf("first environment save: %+v, %v", w, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, configName))
	if !strings.Contains(string(data), `"baseUrl": "http://localhost:8080/v1"`) || !strings.Contains(string(data), `"tenant": "demo"`) {
		t.Errorf("starter config with the saved variable:\n%s", data)
	}

	w, err = s.SaveSettings(bikasend.Settings{Theme: "purple", FontSize: 99, Proxy: "http://proxy:3128"})
	if err != nil || w.Settings.Theme != "system" || w.Settings.FontSize != 13 {
		t.Errorf("settings not normalised: %+v, %v", w.Settings, err)
	}
	if _, err := s.SaveSettings(bikasend.Settings{Proxy: "not a url"}); !errors.Is(err, bikasend.ErrInvalidRequest) {
		t.Errorf("bad proxy: %v", err)
	}
	if got := s.Workspace().Recent; len(got) != 1 || got[0] != root {
		t.Errorf("recent = %v", got)
	}
}

func TestSecretConsoleURL(t *testing.T) {
	s, _ := newTestService(t, "http://x", testConfig, nil)
	if _, err := s.TrustFolder(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetVariable(bikasend.ScopeSession, "typed", bikasend.VariableInput{Kind: bikasend.KindSecret, Value: "abcdef"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		env, name, want string
		wantErr         error
	}{
		{env: "staging", name: "api_key", want: "https://eu-west-1.console.aws.amazon.com/secretsmanager/secret?name=app%2Fkeys&region=eu-west-1"},
		{env: "staging", name: "url", want: "https://eu-west-1.console.aws.amazon.com/systems-manager/parameters/%252Fapp%252Furl/description?region=eu-west-1"},
		{env: "local", name: "tenant", wantErr: bikasend.ErrInvalidVariable},
		{env: "local", name: "typed", wantErr: bikasend.ErrInvalidVariable},
		{env: "local", name: "nope", wantErr: bikasend.ErrUndefinedVariable},
	}
	for _, tt := range tests {
		if _, err := s.SelectEnvironment(tt.env); err != nil {
			t.Fatal(err)
		}
		got, err := s.SecretConsoleURL(tt.name)
		if got != tt.want || !errors.Is(err, tt.wantErr) {
			t.Errorf("%s/%s = %q, %v; want %q, %v", tt.env, tt.name, got, err, tt.want, tt.wantErr)
		}
	}

	if got, err := consoleURL(varValue{AWS: "a"}, ""); err != nil || got != "https://console.aws.amazon.com/secretsmanager/secret?name=a" {
		t.Errorf("no region: %q, %v", got, err)
	}
	for _, region := range []string{"evil.com/x", "eu-west-1\n", "eu-west-1.evil.example", "eu-west-1@evil.example", "EU-WEST-1"} {
		if _, err := consoleURL(varValue{AWS: "a"}, region); !errors.Is(err, bikasend.ErrInvalidConfig) {
			t.Errorf("region %q must be refused: %v", region, err)
		}
	}
	for _, v := range []varValue{{AWS: "a?b#c/../d"}, {AWS: "arn:aws:secretsmanager:eu-west-1:123456789012:secret:x-AbCdEf"}, {SSM: "../x?y#z"}} {
		got, err := consoleURL(v, "eu-west-1")
		u, perr := url.Parse(got)
		if err != nil || perr != nil || u.Host != "eu-west-1.console.aws.amazon.com" || u.Fragment != "" || strings.Contains(u.Path, "?") {
			t.Errorf("hostile name %+v: %q, %v", v, got, err)
		}
	}
}

func TestConfigWritesIgnorePlantedSymlinks(t *testing.T) {
	s, root := newTestService(t, "", "", nil)
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("victim.txt", filepath.Join(root, configName+".tmp")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.SetVariable(bikasend.ScopeFolder, "tenant", bikasend.VariableInput{Kind: bikasend.KindText, Value: "demo"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "keep" {
		t.Errorf("a planted %s.tmp symlink redirected the write: victim = %q", configName, data)
	}
	if fi, err := os.Lstat(filepath.Join(root, configName)); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("%s must be a regular file: %v", configName, err)
	}

	linked, root2 := newTestService(t, "", "", nil)
	if err := os.WriteFile(filepath.Join(root2, "secret.txt"), []byte("API_KEY=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("secret.txt", filepath.Join(root2, configName)); err != nil {
		t.Fatal(err)
	}
	if _, err := linked.Open(root2); err != nil {
		t.Fatal(err)
	}
	if w := linked.Workspace(); w.ConfigError == "" || strings.Contains(w.ConfigError, "API_KEY") {
		t.Errorf("a symlinked %s must be refused without reading it: %q", configName, w.ConfigError)
	}
	if _, err := linked.SetVariable(bikasend.ScopeFolder, "tenant", bikasend.VariableInput{Kind: bikasend.KindText, Value: "demo"}); !errors.Is(err, bikasend.ErrInvalidConfig) {
		t.Errorf("saving through a symlinked %s: %v", configName, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root2, "secret.txt")); string(data) != "API_KEY=x" {
		t.Errorf("secret.txt changed: %q", data)
	}
}

func TestConfigCreateRespectsNewFileAndLocalBase(t *testing.T) {
	s, root := newTestService(t, "", "", nil)
	if err := os.WriteFile(filepath.Join(root, configName), []byte(`{"variables":{"team":"theirs"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetVariable(bikasend.ScopeEnvironment, "x", bikasend.VariableInput{Kind: bikasend.KindText, Value: "1"}); !errors.Is(err, bikasend.ErrInvalidConfig) {
		t.Errorf("a file that appeared after Open must not be overwritten: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, configName)); !strings.Contains(string(data), "theirs") {
		t.Errorf("the team's file was replaced: %s", data)
	}
	if w := s.Workspace(); w.Environment != "" {
		t.Errorf("a failed create must not switch the environment: %q", w.Environment)
	}

	s.specs = []bikasend.Spec{{Servers: []string{"https://evil.example"}}}
	if base := *s.starterConfig().Environments["local"].Variables["baseUrl"].Text; base != "http://localhost:8080" {
		t.Errorf("a non-loopback spec server must not become the local baseUrl: %q", base)
	}
}
