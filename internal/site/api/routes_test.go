package api

import (
	"errors"
	"testing"

	"github.com/BikaSolution/bikasend/pkg/sdk/site"
)

func TestParsePublicURL(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "", want: ""},
		{in: "https://send.example.com", want: "https://send.example.com"},
		{in: "https://send.example.com/", want: "https://send.example.com"},
		{in: "https://send.example.com:8443", want: "https://send.example.com:8443"},
		{in: "http://localhost:8091", want: "http://localhost:8091"},
		{in: "http://send.example.com", wantErr: true},
		{in: "https://send.example.com/path", wantErr: true},
		{in: "https://send.example.com/?q=1", wantErr: true},
		{in: "https://send.example.com/#x", wantErr: true},
		{in: "https://user:pw@send.example.com", wantErr: true},
		{in: "javascript:alert(1)", wantErr: true},
		{in: "send.example.com", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParsePublicURL(tt.in)
		if tt.wantErr != errors.Is(err, site.ErrInvalidConfig) || got != tt.want {
			t.Errorf("ParsePublicURL(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"example/bikasend": "https://github.com/example/bikasend",
		"":                 "",
		"noslash":          "",
		"a/b c":            "https://github.com/a/b%20c",
	} {
		if got := repoURL(in); got != want {
			t.Errorf("repoURL(%q) = %q, want %q", in, got, want)
		}
	}
}
