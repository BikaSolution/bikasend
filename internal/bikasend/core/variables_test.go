package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

func TestVarValueJSON(t *testing.T) {
	tests := []struct {
		in      string
		kind    string
		wantErr bool
	}{
		{in: `"plain"`, kind: bikasend.KindText},
		{in: `{"prompt":"Local key"}`, kind: bikasend.KindPrompt},
		{in: `{"prompt":""}`, kind: bikasend.KindPrompt},
		{in: `{"aws":"app/keys","key":"api"}`, kind: bikasend.KindAWS},
		{in: `{"ssm":"/app/url"}`, kind: bikasend.KindSSM},
		{in: `{"env":"TOKEN"}`, kind: bikasend.KindEnv},
		{in: `{"aws":"a","env":"B"}`, wantErr: true},
		{in: `{"key":"x"}`, wantErr: true},
		{in: `{"ssm":"/a","key":"x"}`, wantErr: true},
		{in: `{"vault":"x"}`, wantErr: true},
		{in: `42`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var v varValue
			err := json.Unmarshal([]byte(tt.in), &v)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr {
				return
			}
			if v.kind() != tt.kind {
				t.Errorf("kind = %s, want %s", v.kind(), tt.kind)
			}
			out, _ := json.Marshal(v)
			if string(out) != tt.in {
				t.Errorf("round trip = %s, want %s", out, tt.in)
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		global  bool
		wantErr error
	}{
		{name: "valid", in: `{"variables":{"tenant":"demo","api_key":"{{other}}"},"environments":{"staging":{"hosts":["*.example.com","api.example.com:8443"],"aws":{"profile":"p"},"tls":{"caFile":"certs/ca.pem"},"variables":{"token":{"aws":"x"}}}}}`},
		{name: "unknown field", in: `{"variabls":{}}`, wantErr: bikasend.ErrInvalidConfig},
		{name: "secret in plain text", in: `{"variables":{"api_key":"abc123"}}`, wantErr: bikasend.ErrSecretInFile},
		{name: "secret in env plain text", in: `{"environments":{"x":{"variables":{"password":"hunter2"}}}}`, wantErr: bikasend.ErrSecretInFile},
		{name: "tls path outside folder", in: `{"environments":{"x":{"tls":{"keyFile":"../../.ssh/id_rsa"}}}}`, wantErr: bikasend.ErrInvalidConfig},
		{name: "absolute tls path", in: `{"environments":{"x":{"tls":{"caFile":"/etc/ca.pem"}}}}`, wantErr: bikasend.ErrInvalidConfig},
		{name: "bad host", in: `{"environments":{"x":{"hosts":["https://a.com"]}}}`, wantErr: bikasend.ErrInvalidConfig},
		{name: "bad variable name", in: `{"variables":{"a b":"x"}}`, wantErr: bikasend.ErrInvalidConfig},
		{name: "oauth2 without token url", in: `{"environments":{"x":{"oauth2":{"clientId":"c"}}}}`, wantErr: bikasend.ErrInvalidConfig},
		{name: "proxy is not allowed in a shared file", in: `{"environments":{"x":{"proxy":"http://evil:8080"}}}`, wantErr: bikasend.ErrInvalidConfig},
		{name: "globals hold variables only", in: `{"environments":{"x":{}}}`, global: true, wantErr: bikasend.ErrInvalidConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig([]byte(tt.in), tt.global)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestExpansion(t *testing.T) {
	vars := map[string]binding{
		"baseUrl": {value: "https://{{host}}/v1", found: true},
		"host":    {value: "api.example.com", found: true},
		"key":     {value: "s3cr3t-value", secret: true, found: true},
		"a":       {value: "{{b}}", found: true},
		"b":       {value: "{{a}}", found: true},
	}
	lookup := func(n string) binding {
		if b, ok := vars[n]; ok {
			return b
		}
		return binding{reason: "is not defined in any scope"}
	}

	e := newExpansion(lookup)
	got := e.expand("{{ baseUrl }}/items?k={{key}}&lit={{{{raw}}}}")
	if got != "https://api.example.com/v1/items?k=s3cr3t-value&lit={{raw}}" {
		t.Errorf("expand = %q", got)
	}
	if !reflect.DeepEqual(e.used, []string{"baseUrl", "host", "key"}) || e.problem() != nil {
		t.Errorf("used = %v, problem = %v", e.used, e.problem())
	}
	if m := e.mask(got); m != "https://api.example.com/v1/items?k=••••••&lit={{raw}}" {
		t.Errorf("mask = %q", m)
	}
	escaped := newExpansion(func(string) binding { return binding{value: "a+b/c=d e", secret: true, found: true} })
	escaped.expand("{{k}}")
	if m := escaped.mask("q=a%2Bb%2Fc%3Dd+e p=a+b%2Fc=d%20e j=a+b\\/c=d e"); strings.Contains(m, "a") {
		t.Errorf("escaped secret forms not masked: %q", m)
	}

	missing := newExpansion(lookup)
	missing.expand("{{nope}}")
	if !errors.Is(missing.problem(), bikasend.ErrUndefinedVariable) {
		t.Errorf("missing: %v", missing.problem())
	}

	cycle := newExpansion(lookup)
	cycle.expand("{{a}}")
	if err := cycle.problem(); !errors.Is(err, bikasend.ErrVariableCycle) || err.Error() != "bikasend: variable cycle: a → b → a" {
		t.Errorf("cycle: %v", err)
	}
}

func TestSecretNameAndHosts(t *testing.T) {
	for name, want := range map[string]bool{"api_key": true, "X-API-Key": true, "token": true, "password": true, "tokenUrl": false, "baseUrl": false, "tenant": false, "authHost": false} {
		if secretName(name) != want {
			t.Errorf("secretName(%q) = %v", name, !want)
		}
	}
	hosts := []string{"*.staging.example.com", "auth.example.com"}
	for host, want := range map[string]bool{"api.staging.example.com": true, "staging.example.com": false, "AUTH.example.com": true, "evil.com": false, "staging.example.com.evil.com": false} {
		if matchHost(hosts, host) != want {
			t.Errorf("matchHost(%q) = %v", host, !want)
		}
	}
}
