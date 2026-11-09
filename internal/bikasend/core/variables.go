package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

const (
	configName = ".bika.json"
	maxConfig  = 1 << 20
	maskText   = "••••••"
)

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	hostPattern = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?$`)
	// {{{{text}}}} sends a literal {{text}}; {{ name }} is a variable.
	varRef = regexp.MustCompile(`\{\{\{\{([^{}]*)\}\}\}\}|\{\{\s*([A-Za-z0-9_.$-]+)\s*\}\}`)
)

type configFile struct {
	Variables    map[string]varValue    `json:"variables,omitempty"`
	Environments map[string]environment `json:"environments,omitempty"`
}

type environment struct {
	Hosts           []string            `json:"hosts,omitempty"`
	AWS             *awsConfig          `json:"aws,omitempty"`
	TimeoutSeconds  int                 `json:"timeoutSeconds,omitempty"`
	FollowRedirects *bool               `json:"followRedirects,omitempty"`
	TLS             *tlsConfig          `json:"tls,omitempty"`
	OAuth2          *oauth2Config       `json:"oauth2,omitempty"`
	SigV4           *sigv4Config        `json:"sigv4,omitempty"`
	Variables       map[string]varValue `json:"variables,omitempty"`
}

type awsConfig struct {
	Profile string `json:"profile,omitempty"`
	Region  string `json:"region,omitempty"`
}

// TLS paths are relative to the folder root and are read through os.Root.
type tlsConfig struct {
	CAFile   string `json:"caFile,omitempty"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
}

type oauth2Config struct {
	TokenURL     string   `json:"tokenUrl"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret"`
	Scopes       []string `json:"scopes,omitempty"`
}

type sigv4Config struct {
	Service string `json:"service"`
	Region  string `json:"region,omitempty"`
}

// varValue is a plain string or one of {"prompt"}, {"aws","key"}, {"ssm"}, {"env"}.
type varValue struct {
	Text     *string
	IsPrompt bool
	Prompt   string
	AWS      string
	SSM      string
	Key      string
	Env      string
}

func (v *varValue) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v.Text = &s
		return nil
	}
	var o struct {
		Prompt *string `json:"prompt"`
		AWS    string  `json:"aws"`
		SSM    string  `json:"ssm"`
		Key    string  `json:"key"`
		Env    string  `json:"env"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return fmt.Errorf("a variable is a string or one of prompt, aws, ssm, env: %w", err)
	}
	sources := 0
	for _, set := range []bool{o.Prompt != nil, o.AWS != "", o.SSM != "", o.Env != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return errors.New("a variable needs exactly one of prompt, aws, ssm, env")
	}
	if o.Key != "" && o.AWS == "" {
		return errors.New(`"key" only works with "aws"`)
	}
	*v = varValue{IsPrompt: o.Prompt != nil, AWS: o.AWS, SSM: o.SSM, Key: o.Key, Env: o.Env}
	if o.Prompt != nil {
		v.Prompt = *o.Prompt
	}
	return nil
}

func (v varValue) MarshalJSON() ([]byte, error) {
	switch v.kind() {
	case bikasend.KindText:
		return json.Marshal(*v.Text)
	case bikasend.KindPrompt:
		return json.Marshal(map[string]string{"prompt": v.Prompt})
	case bikasend.KindAWS:
		m := map[string]string{"aws": v.AWS}
		if v.Key != "" {
			m["key"] = v.Key
		}
		return json.Marshal(m)
	case bikasend.KindSSM:
		return json.Marshal(map[string]string{"ssm": v.SSM})
	}
	return json.Marshal(map[string]string{"env": v.Env})
}

func (v varValue) kind() string {
	switch {
	case v.Text != nil:
		return bikasend.KindText
	case v.IsPrompt:
		return bikasend.KindPrompt
	case v.AWS != "":
		return bikasend.KindAWS
	case v.SSM != "":
		return bikasend.KindSSM
	}
	return bikasend.KindEnv
}

func (v varValue) ref() string {
	switch v.kind() {
	case bikasend.KindPrompt:
		return "asked each session"
	case bikasend.KindAWS:
		if v.Key != "" {
			return "aws:" + v.AWS + "#" + v.Key
		}
		return "aws:" + v.AWS
	case bikasend.KindSSM:
		return "ssm:" + v.SSM
	case bikasend.KindEnv:
		return "env:" + v.Env
	}
	return ""
}

func parseConfig(data []byte, global bool) (configFile, error) {
	var c configFile
	if len(data) > maxConfig {
		return c, errors.Join(bikasend.ErrInvalidConfig, errors.New("file is larger than 1 MB"))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, errors.Join(bikasend.ErrInvalidConfig, err)
	}
	if global && len(c.Environments) > 0 {
		return c, errors.Join(bikasend.ErrInvalidConfig, errors.New("globals.json holds variables only"))
	}
	if err := validateVars(c.Variables); err != nil {
		return c, err
	}
	for name, env := range c.Environments {
		if !namePattern.MatchString(name) {
			return c, errors.Join(bikasend.ErrInvalidConfig, fmt.Errorf("environment name %q", name))
		}
		for _, h := range env.Hosts {
			if !hostPattern.MatchString(h) {
				return c, errors.Join(bikasend.ErrInvalidConfig, fmt.Errorf("%s: host %q", name, h))
			}
		}
		if env.TLS != nil {
			for _, p := range []string{env.TLS.CAFile, env.TLS.CertFile, env.TLS.KeyFile} {
				if p != "" && !fs.ValidPath(p) {
					return c, errors.Join(bikasend.ErrInvalidConfig, fmt.Errorf("%s: TLS file %q must be a path inside the folder", name, p))
				}
			}
		}
		if env.OAuth2 != nil && (env.OAuth2.TokenURL == "" || env.OAuth2.ClientID == "") {
			return c, errors.Join(bikasend.ErrInvalidConfig, fmt.Errorf("%s: oauth2 needs tokenUrl and clientId", name))
		}
		if env.SigV4 != nil && env.SigV4.Service == "" {
			return c, errors.Join(bikasend.ErrInvalidConfig, fmt.Errorf("%s: sigv4 needs a service", name))
		}
		if env.TimeoutSeconds < 0 || env.TimeoutSeconds > 600 {
			return c, errors.Join(bikasend.ErrInvalidConfig, fmt.Errorf("%s: timeoutSeconds must be 0-600", name))
		}
		if err := validateVars(env.Variables); err != nil {
			return c, err
		}
	}
	for name, v := range c.Variables {
		if err := guardFileValue(name, v); err != nil {
			return c, err
		}
	}
	for _, env := range c.Environments {
		for name, v := range env.Variables {
			if err := guardFileValue(name, v); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

func validateVars(vars map[string]varValue) error {
	for name := range vars {
		if !namePattern.MatchString(name) {
			return errors.Join(bikasend.ErrInvalidConfig, fmt.Errorf("variable name %q", name))
		}
	}
	return nil
}

// guardFileValue refuses a plain-text value with a secret-looking name in a file.
func guardFileValue(name string, v varValue) error {
	if v.kind() == bikasend.KindText && secretName(name) && *v.Text != "" && !isOnlyReference(*v.Text) {
		return fmt.Errorf("%w: %q looks secret; keep it in the session, ask each session, or reference AWS", bikasend.ErrSecretInFile, name)
	}
	return nil
}

func isOnlyReference(s string) bool {
	return varRef.ReplaceAllString(s, "") == ""
}

func marshalConfig(c configFile) ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func secretName(name string) bool {
	n := strings.ToLower(name)
	for _, suffix := range []string{"url", "uri", "host", "endpoint", "path"} {
		if strings.HasSuffix(n, suffix) {
			return false
		}
	}
	for _, word := range []string{"key", "token", "secret", "password", "passwd", "credential", "auth", "cookie", "private"} {
		if strings.Contains(n, word) {
			return true
		}
	}
	return false
}

func isSecretKind(kind string) bool {
	return kind != bikasend.KindText
}

// binding is one variable as the resolver sees it.
type binding struct {
	value  string
	secret bool
	scope  string
	found  bool
	reason string
}

// expansion resolves {{variables}} and records what it used.
type expansion struct {
	lookup  func(name string) binding
	used    []string
	missing map[string]string
	secrets map[string]string
	err     error
}

func newExpansion(lookup func(string) binding) *expansion {
	return &expansion{lookup: lookup, missing: map[string]string{}, secrets: map[string]string{}}
}

func (e *expansion) expand(s string) string {
	return e.walk(s, nil)
}

func (e *expansion) walk(s string, stack []string) string {
	return varRef.ReplaceAllStringFunc(s, func(m string) string {
		sub := varRef.FindStringSubmatch(m)
		if strings.HasPrefix(m, "{{{{") {
			return "{{" + sub[1] + "}}"
		}
		name := sub[2]
		if !slices.Contains(e.used, name) {
			e.used = append(e.used, name)
		}
		if slices.Contains(stack, name) {
			if e.err == nil {
				e.err = fmt.Errorf("%w: %s → %s", bikasend.ErrVariableCycle, strings.Join(stack, " → "), name)
			}
			return m
		}
		if len(stack) >= 10 {
			if e.err == nil {
				e.err = fmt.Errorf("%w: nested more than 10 levels at %s", bikasend.ErrVariableCycle, name)
			}
			return m
		}
		b := e.lookup(name)
		if !b.found {
			e.missing[name] = b.reason
			return m
		}
		v := e.walk(b.value, append(stack, name))
		if b.secret && v != "" {
			e.secrets[name] = v
		}
		return v
	})
}

func (e *expansion) problem() error {
	if e.err != nil {
		return e.err
	}
	if len(e.missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(e.missing))
	for n := range e.missing {
		names = append(names, n)
	}
	slices.Sort(names)
	var parts []string
	for _, n := range names {
		parts = append(parts, "{{"+n+"}} "+e.missing[n])
	}
	return fmt.Errorf("%w: %s", bikasend.ErrUndefinedVariable, strings.Join(parts, "; "))
}

// mask hides every secret value in s, also in its URL-escaped forms.
func (e *expansion) mask(s string) string {
	for _, v := range e.sortedSecrets() {
		for _, form := range secretForms(v) {
			s = strings.ReplaceAll(s, form, maskText)
		}
	}
	return s
}

// secretForms is v as it can appear in a URL, a form body or a JSON echo.
func secretForms(v string) []string {
	forms := []string{v}
	for _, f := range []string{url.QueryEscape(v), url.PathEscape(v), strings.ReplaceAll(v, "/", `\/`)} {
		if !slices.Contains(forms, f) {
			forms = append(forms, f)
		}
	}
	return forms
}

// sortedSecrets is longest first, so a secret that contains another is masked whole.
func (e *expansion) sortedSecrets() []string {
	values := make([]string, 0, len(e.secrets))
	for _, v := range e.secrets {
		if len(v) >= 4 {
			values = append(values, v)
		}
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	return values
}

func envValue(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	return v, ok && v != ""
}

func matchHost(patterns []string, host string) bool {
	host = strings.ToLower(host)
	for _, p := range patterns {
		p = strings.ToLower(p)
		if p == host {
			return true
		}
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(host, p[1:]) {
			return true
		}
	}
	return false
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}
