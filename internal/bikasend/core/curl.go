package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

var shellName = regexp.MustCompile(`[^A-Z0-9_]`)

// Curl renders the resolved request as a curl command. Secret values become $ENV references.
func (s *Service) Curl(r bikasend.Request) (string, error) {
	s.mu.Lock()
	b, err := s.build(context.Background(), r)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if b.preview.Error != "" {
		return "", fmt.Errorf("%w: %s", bikasend.ErrUndefinedVariable, b.preview.Error)
	}

	envs := map[string]string{}
	for name, value := range b.exp.secrets {
		envs[value] = envVarName(name)
	}
	values := make([]string, 0, len(envs))
	for v := range envs {
		if len(v) >= 4 {
			values = append(values, v)
		}
	}
	slices.SortFunc(values, func(a, c string) int { return len(c) - len(a) })
	quote := func(arg string) string { return shellQuote(arg, values, envs) }

	args := []string{"curl", "-X", b.req.Method, quote(b.req.URL.String())}
	for _, k := range sortedHeaderKeys(b.req.Header) {
		if r.BodyType == "multipart" && k == "Content-Type" {
			continue
		}
		for _, v := range b.req.Header[k] {
			args = append(args, "-H", quote(k+": "+v))
		}
	}
	switch r.BodyType {
	case "json", "text", "xml", "form":
		if len(b.body) > 0 {
			args = append(args, "--data-raw", quote(string(b.body)))
		}
	case "multipart":
		for _, f := range r.Form {
			if !f.Enabled || f.Name == "" {
				continue
			}
			v := b.exp.expand(f.Value)
			if f.File {
				v = "@" + v
			}
			args = append(args, "-F", quote(b.exp.expand(f.Name)+"="+v))
		}
	}

	var head []string
	if len(envs) > 0 {
		names := make([]string, 0, len(envs))
		for _, n := range envs {
			names = append(names, n)
		}
		slices.Sort(names)
		head = append(head, "# Set first: "+strings.Join(names, ", "))
	}
	switch r.Auth {
	case "oauth2":
		head = append(head, "# Uses OAuth2: add -H 'Authorization: Bearer <token>'")
	case "sigv4":
		head = append(head, "# Uses AWS SigV4: add --aws-sigv4 and --user, or sign it another way")
	}
	cmd := strings.Join(args, " \\\n  ")
	if len(head) > 0 {
		cmd = strings.Join(head, "\n") + "\n" + cmd
	}
	return cmd, nil
}

func envVarName(name string) string {
	n := shellName.ReplaceAllString(strings.ToUpper(name), "_")
	if n == "" || unicode.IsDigit(rune(n[0])) || n[0] == '_' {
		n = "BIKASEND" + "_" + strings.TrimLeft(n, "_")
	}
	return n
}

// shellQuote single-quotes arg and swaps each secret value for "$NAME".
func shellQuote(arg string, secrets []string, names map[string]string) string {
	type part struct {
		text   string
		secret bool
	}
	parts := []part{{text: arg}}
	for _, v := range secrets {
		var next []part
		for _, p := range parts {
			if p.secret {
				next = append(next, p)
				continue
			}
			pieces := strings.Split(p.text, v)
			for i, piece := range pieces {
				if i > 0 {
					next = append(next, part{text: names[v], secret: true})
				}
				if piece != "" {
					next = append(next, part{text: piece})
				}
			}
		}
		parts = next
	}
	var b strings.Builder
	for _, p := range parts {
		if p.secret {
			b.WriteString(`"$` + p.text + `"`)
			continue
		}
		b.WriteString("'" + strings.ReplaceAll(p.text, "'", `'\''`) + "'")
	}
	if b.Len() == 0 {
		return "''"
	}
	return b.String()
}

// ImportCurl turns a curl command into a request template.
func (s *Service) ImportCurl(cmd string) (bikasend.Request, error) {
	args, err := shellSplit(cmd)
	if err != nil {
		return bikasend.Request{}, err
	}
	if len(args) == 0 || args[0] != "curl" {
		return bikasend.Request{}, fmt.Errorf("%w: it must start with curl", bikasend.ErrCurl)
	}
	r := bikasend.Request{Method: "", BodyType: "none", PathParams: []bikasend.KV{}, Query: []bikasend.KV{}, Form: []bikasend.Field{}}
	var headers, data []string
	getMode := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, inline, hasInline := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "--") || !hasInline {
			name, inline = a, ""
		}
		value := func() (string, error) {
			if hasInline && strings.HasPrefix(a, "--") {
				return inline, nil
			}
			if len(name) > 2 && name[0] == '-' && name[1] != '-' {
				return name[2:], nil // -XPOST
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%w: %s needs a value", bikasend.ErrCurl, name)
			}
			i++
			return args[i], nil
		}
		short := name
		if len(name) > 2 && name[0] == '-' && name[1] != '-' {
			short = name[:2]
		}
		switch short {
		case "-X", "--request":
			v, err := value()
			if err != nil {
				return r, err
			}
			r.Method = strings.ToUpper(v)
		case "-H", "--header":
			v, err := value()
			if err != nil {
				return r, err
			}
			headers = append(headers, v)
		case "-d", "--data", "--data-raw", "--data-binary", "--data-ascii", "--data-urlencode", "--json":
			v, err := value()
			if err != nil {
				return r, err
			}
			if strings.HasPrefix(v, "@") && short != "--data-raw" {
				return r, fmt.Errorf("%w: reading the body from a file (%s) is not supported; paste the body instead", bikasend.ErrCurl, v)
			}
			if short == "--json" {
				headers = append(headers, "Content-Type: application/json", "Accept: application/json")
			}
			data = append(data, v)
		case "-F", "--form":
			v, err := value()
			if err != nil {
				return r, err
			}
			fname, fval, _ := strings.Cut(v, "=")
			f := bikasend.Field{Name: fname, Value: fval, Enabled: true}
			if path, ok := strings.CutPrefix(fval, "@"); ok {
				f.File, f.Value = true, path
			}
			r.Form = append(r.Form, f)
			r.BodyType = "multipart"
		case "-u", "--user":
			v, err := value()
			if err != nil {
				return r, err
			}
			headers = append(headers, "Authorization: Basic "+basicAuth(v))
		case "--url":
			v, err := value()
			if err != nil {
				return r, err
			}
			r.URL = v
		case "-G", "--get":
			getMode = true
		case "-A", "--user-agent":
			v, err := value()
			if err != nil {
				return r, err
			}
			headers = append(headers, "User-Agent: "+v)
		case "-b", "--cookie":
			v, err := value()
			if err != nil {
				return r, err
			}
			headers = append(headers, "Cookie: "+v)
		case "-e", "--referer":
			v, err := value()
			if err != nil {
				return r, err
			}
			headers = append(headers, "Referer: "+v)
		case "-o", "--output", "-m", "--max-time", "--connect-timeout", "-w", "--write-out", "--retry":
			if _, err := value(); err != nil {
				return r, err
			}
		default:
			if strings.HasPrefix(a, "-") {
				continue // flags such as -s, -v, -L, -k, --compressed do not change the request
			}
			if r.URL == "" {
				r.URL = a
			}
		}
	}
	if r.URL == "" {
		return r, fmt.Errorf("%w: no URL found", bikasend.ErrCurl)
	}
	body := strings.Join(data, "&")
	switch {
	case getMode && body != "":
		sep := "?"
		if strings.Contains(r.URL, "?") {
			sep = "&"
		}
		r.URL += sep + body
	case body != "":
		r.Body = body
		r.BodyType = "text"
		ct := ""
		for _, h := range headers {
			if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Type") {
				ct = strings.TrimSpace(v)
			}
		}
		switch {
		case isJSONType(ct) || (ct == "" && json.Valid([]byte(body))):
			r.BodyType = "json"
			if ct == "" {
				headers = append(headers, "Content-Type: application/json")
			}
		case strings.Contains(ct, "xml"):
			r.BodyType = "xml"
		case ct == "":
			headers = append(headers, "Content-Type: application/x-www-form-urlencoded")
		}
	}
	if r.Method == "" {
		r.Method = "GET"
		if body != "" && !getMode || r.BodyType == "multipart" {
			r.Method = "POST"
		}
	}
	if u, err := url.Parse(r.URL); err != nil || u.Host == "" {
		return r, fmt.Errorf("%w: %q is not a URL", bikasend.ErrCurl, r.URL)
	}
	r.Headers = strings.Join(headers, "\n")
	return r, nil
}

func basicAuth(userpass string) string {
	return base64.StdEncoding.EncodeToString([]byte(userpass))
}

// shellSplit splits a POSIX-style command line: quotes, escapes and line continuations.
func shellSplit(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && (s[i+1] == '\n' || s[i+1] == '\r'):
			i++
			if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inArg = true
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("%w: unterminated single quote", bikasend.ErrCurl)
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inArg = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.ContainsRune(`"\$`+"`", rune(s[i+1])) {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, fmt.Errorf("%w: unterminated double quote", bikasend.ErrCurl)
			}
			inArg = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
