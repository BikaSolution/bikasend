package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

var shellName = regexp.MustCompile(`[^A-Z0-9_]`)

// segment is one piece of an exported string: literal text, or an environment variable that
// holds a secret. Exports never contain secret values.
type segment struct {
	text, env string
}

type value []segment

type exportHeader struct{ name, value value }

type exportField struct {
	name, value value
	file        bool
	path        string
}

// exportPlan is a resolved request, ready to be written in any language.
type exportPlan struct {
	method      string
	url         value
	headers     []exportHeader
	contentType value
	body        value
	multipart   bool
	fields      []exportField
	envs        []string
	auth        string
}

func lit(s string) value { return value{{text: s}} }

// Export writes the request as a command or snippet: curl, powershell, httpie, fetch or python.
// Every piece is quoted for its language; a request that a format cannot carry safely is refused.
func (s *Service) Export(r bikasend.Request, format string) (string, error) {
	render, ok := exporters[format]
	if !ok {
		return "", fmt.Errorf("%w: unknown export format %q", bikasend.ErrInvalidRequest, format)
	}
	s.mu.Lock()
	b, err := s.build(context.Background(), r)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if b.preview.Error != "" {
		return "", fmt.Errorf("%w: %s", bikasend.ErrUndefinedVariable, b.preview.Error)
	}

	// One variable per secret value; distinct names that collapse to the same variable get a suffix.
	names := map[string]string{}
	used := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(b.exp.secrets)) {
		v := b.exp.secrets[name]
		if _, ok := names[v]; ok {
			continue
		}
		env := envVarName(name)
		for i := 2; used[env]; i++ {
			env = envVarName(name) + "_" + strconv.Itoa(i)
		}
		used[env] = true
		names[v] = env
	}
	secrets := make([]string, 0, len(names))
	for v := range names {
		if len(v) >= 4 {
			secrets = append(secrets, v)
		}
	}
	slices.SortFunc(secrets, func(a, c string) int { return len(c) - len(a) })
	split := func(s string) value { return splitSecrets(s, secrets, names) }

	p := exportPlan{method: b.req.Method, url: split(b.req.URL.String()), auth: r.Auth}
	for _, k := range sortedHeaderKeys(b.req.Header) {
		if !isToken(k) {
			return "", fmt.Errorf("%w: header name %q is not valid", bikasend.ErrInvalidRequest, k)
		}
		v := strings.Join(b.req.Header[k], ", ")
		if k == "Content-Type" {
			if r.BodyType != "multipart" {
				p.contentType = split(v)
			}
			continue
		}
		p.headers = append(p.headers, exportHeader{name: split(k), value: split(v)})
	}
	switch r.BodyType {
	case "json", "text", "xml", "form":
		if len(b.body) > 0 {
			p.body = split(string(b.body))
		}
	case "multipart":
		p.multipart = true
		for _, f := range r.Form {
			if !f.Enabled || f.Name == "" {
				continue
			}
			field := exportField{name: split(b.exp.expand(f.Name)), file: f.File}
			if f.File {
				field.path = b.exp.expand(f.Value)
			} else {
				field.value = split(b.exp.expand(f.Value))
			}
			p.fields = append(p.fields, field)
		}
	}
	for _, n := range names {
		if !slices.Contains(p.envs, n) {
			p.envs = append(p.envs, n)
		}
	}
	slices.Sort(p.envs)
	out, err := render(p)
	if err != nil {
		return "", err
	}
	// The secret must not reach the clipboard in any form: raw, URL-escaped (query tables, form
	// bodies, path parameters) or too short to replace.
	for _, v := range b.exp.secrets {
		for _, f := range secretForms(v) {
			if strings.Contains(out, f) {
				return "", fmt.Errorf("%w: a secret would appear in the export (escaped or too short to replace); copy it without that secret", bikasend.ErrInvalidRequest)
			}
		}
	}
	if r := unsafeRune(out); r != 0 {
		return "", fmt.Errorf("%w: the request holds control character U+%04X, which a pasted command must not carry", bikasend.ErrInvalidRequest, r)
	}
	return out, nil
}

// unsafeRune finds characters that can hijack a terminal paste: C0 and C1 controls (except tab
// and newline) and bidirectional overrides that hide what a line does.
func unsafeRune(s string) rune {
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n':
		case r < 0x20, r >= 0x7f && r <= 0x9f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return r
		}
	}
	return 0
}

// isToken reports whether s is a valid HTTP header name (RFC 9110 token).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum && !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}

var exporters = map[string]func(exportPlan) (string, error){
	"curl":       exportCurl,
	"httpie":     exportHTTPie,
	"powershell": exportPowerShell,
	"fetch":      exportFetch,
	"python":     exportPython,
}

// envVarName always carries the BIKASEND_ prefix, so a variable named path or GITHUB_TOKEN can
// never pick up the user's real $PATH or token from their shell.
func envVarName(name string) string {
	n := strings.Trim(shellName.ReplaceAllString(strings.ToUpper(name), "_"), "_")
	if n == "" {
		n = "SECRET"
	}
	return "BIKASEND_" + n
}

// splitSecrets cuts s at every secret value, which becomes a reference to its variable.
func splitSecrets(s string, secrets []string, names map[string]string) value {
	parts := lit(s)
	for _, v := range secrets {
		var next value
		for _, p := range parts {
			if p.env != "" {
				next = append(next, p)
				continue
			}
			for i, piece := range strings.Split(p.text, v) {
				if i > 0 {
					next = append(next, segment{env: names[v]})
				}
				if piece != "" {
					next = append(next, segment{text: piece})
				}
			}
		}
		parts = next
	}
	return parts
}

// join concatenates values, merging neighbouring text so each piece is quoted once.
func join(vs ...value) value {
	var out value
	for _, v := range vs {
		for _, p := range v {
			if n := len(out); n > 0 && p.env == "" && out[n-1].env == "" {
				out[n-1].text += p.text
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

// header lines for shells and scripts: "# Set first: …" plus auth notes.
func exportNotes(p exportPlan, comment, envRef string) []string {
	var out []string
	if len(p.envs) > 0 {
		refs := make([]string, len(p.envs))
		for i, n := range p.envs {
			refs[i] = fmt.Sprintf(envRef, n)
		}
		out = append(out, comment+" Set first: "+strings.Join(refs, ", "))
	}
	switch p.auth {
	case "oauth2":
		out = append(out, comment+" Uses OAuth2: add an Authorization: Bearer <token> header")
	case "sigv4":
		out = append(out, comment+" Uses AWS SigV4: sign the request with your AWS credentials")
	}
	return out
}

// sh quotes for POSIX shells: 'text' and "$NAME", side by side.
func sh(v value) string {
	var b strings.Builder
	for _, p := range v {
		if p.env != "" {
			b.WriteString(`"$` + p.env + `"`)
			continue
		}
		b.WriteString("'" + strings.ReplaceAll(p.text, "'", `'\''`) + "'")
	}
	if b.Len() == 0 {
		return "''"
	}
	return b.String()
}

// curlPath quotes a file name for curl's -F parser, so ; and , in it stay part of the name.
func curlPath(path string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
}

func exportCurl(p exportPlan) (string, error) {
	// --globoff: [] and {} in a URL from a spec must not fan out into many requests.
	lines := []string{"curl --globoff -X " + sh(lit(p.method)) + " " + sh(p.url)}
	if len(p.contentType) > 0 {
		lines = append(lines, "-H "+sh(join(lit("Content-Type: "), p.contentType)))
	}
	for _, h := range p.headers {
		lines = append(lines, "-H "+sh(join(h.name, lit(": "), h.value)))
	}
	if len(p.body) > 0 {
		lines = append(lines, "--data-raw "+sh(p.body))
	}
	for _, f := range p.fields {
		if hasAny(f.name, "=;") {
			return "", fmt.Errorf("%w: a form field name holds = or ;, which curl cannot carry", bikasend.ErrInvalidRequest)
		}
		if f.file {
			lines = append(lines, "-F "+sh(join(f.name, lit("=@"+curlPath(f.path)))))
		} else {
			// --form-string: a value starting with @ or < stays text and never reads a local file.
			lines = append(lines, "--form-string "+sh(join(f.name, lit("="), f.value)))
		}
	}
	return withNotes(exportNotes(p, "#", "%s"), strings.Join(lines, " \\\n  ")), nil
}

// hasAny reports whether any literal piece of v contains one of chars.
func hasAny(v value, chars string) bool {
	return slices.ContainsFunc(v, func(s segment) bool { return strings.ContainsAny(s.text, chars) })
}

// startsWithAny reports whether v begins with literal text that starts with one of chars.
func startsWithAny(v value, chars string) bool {
	return len(v) > 0 && v[0].env == "" && v[0].text != "" && strings.ContainsRune(chars, rune(v[0].text[0]))
}

// errHTTPie explains a refusal: HTTPie reads separators and @ from item text, so it cannot carry
// every value as plain text the way curl can.
var errHTTPie = fmt.Errorf("%w: HTTPie would read part of this request as a file or a separator; copy it as cURL instead", bikasend.ErrInvalidRequest)

func exportHTTPie(p exportPlan) (string, error) {
	lines := []string{"http --ignore-stdin " + sh(lit(p.method)) + " " + sh(p.url)}
	if p.multipart {
		lines[0] = "http --ignore-stdin --multipart " + sh(lit(p.method)) + " " + sh(p.url)
	}
	const separators = ":=@;\\"
	if len(p.contentType) > 0 {
		if startsWithAny(p.contentType, "@=:") {
			return "", errHTTPie
		}
		lines = append(lines, sh(join(lit("Content-Type:"), p.contentType)))
	}
	for _, h := range p.headers {
		if hasAny(h.name, separators) || startsWithAny(h.value, "@=:") {
			return "", errHTTPie
		}
		lines = append(lines, sh(join(h.name, lit(":"), h.value)))
	}
	if len(p.body) > 0 {
		lines = append(lines, "--raw "+sh(p.body))
	}
	for _, f := range p.fields {
		if hasAny(f.name, separators) || (f.file && strings.ContainsAny(f.path, ";\\")) || (!f.file && startsWithAny(f.value, "@=:")) {
			return "", errHTTPie
		}
		if f.file {
			lines = append(lines, sh(join(f.name, lit("@"+f.path))))
		} else {
			lines = append(lines, sh(join(f.name, lit("="), f.value)))
		}
	}
	return withNotes(exportNotes(p, "#", "%s"), strings.Join(lines, " \\\n  ")), nil
}

// psQuotes are every character PowerShell reads as a single quote; each one doubles to escape.
var psQuotes = strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")

func ps(v value) string {
	parts := make([]string, 0, len(v))
	for _, p := range v {
		if p.env != "" {
			parts = append(parts, "$env:"+p.env)
			continue
		}
		parts = append(parts, "'"+psQuotes.Replace(p.text)+"'")
	}
	switch len(parts) {
	case 0:
		return "''"
	case 1:
		return parts[0]
	}
	return "(" + strings.Join(parts, " + ") + ")"
}

func exportPowerShell(p exportPlan) (string, error) {
	var out []string
	out = append(out, exportNotes(p, "#", "$env:%s")...)
	if len(p.headers) > 0 {
		out = append(out, "$headers = @{")
		for _, h := range p.headers {
			out = append(out, "  "+ps(h.name)+" = "+ps(h.value))
		}
		out = append(out, "}")
	}
	call := "Invoke-RestMethod -Method " + ps(lit(p.method)) + " -Uri " + ps(p.url)
	if len(p.headers) > 0 {
		call += " -Headers $headers"
	}
	if len(p.contentType) > 0 {
		call += " -ContentType " + ps(p.contentType)
	}
	if len(p.body) > 0 {
		out = append(out, "$body = "+ps(p.body))
		call += " -Body $body"
	}
	if len(p.fields) > 0 {
		out = append(out, "# -Form needs PowerShell 7 or later", "$form = @{")
		for _, f := range p.fields {
			if f.file {
				out = append(out, "  "+ps(f.name)+" = Get-Item -LiteralPath "+ps(lit(f.path)))
			} else {
				out = append(out, "  "+ps(f.name)+" = "+ps(f.value))
			}
		}
		out = append(out, "}")
		call += " -Form $form"
	}
	return strings.Join(append(out, call), "\n"), nil
}

// jsonString is a double-quoted literal that JavaScript and Python both read the same way.
func jsonString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func code(v value, env func(string) string) string {
	parts := make([]string, 0, len(v))
	for _, p := range v {
		if p.env != "" {
			parts = append(parts, env(p.env))
			continue
		}
		parts = append(parts, jsonString(p.text))
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " + ")
}

func exportFetch(p exportPlan) (string, error) {
	js := func(v value) string { return code(v, func(n string) string { return "process.env." + n }) }
	out := exportNotes(p, "//", "%s")
	var opts []string
	opts = append(opts, "  method: "+js(lit(p.method))+",")
	if len(p.headers) > 0 || len(p.contentType) > 0 {
		opts = append(opts, "  headers: {")
		if len(p.contentType) > 0 {
			opts = append(opts, `    "Content-Type": `+js(p.contentType)+",")
		}
		for _, h := range p.headers {
			key := js(h.name)
			if len(h.name) != 1 || h.name[0].env != "" {
				key = "[" + key + "]"
			}
			opts = append(opts, "    "+key+": "+js(h.value)+",")
		}
		opts = append(opts, "  },")
	}
	if len(p.body) > 0 {
		opts = append(opts, "  body: "+js(p.body)+",")
	}
	if len(p.fields) > 0 {
		if slices.ContainsFunc(p.fields, func(f exportField) bool { return f.file }) {
			out = append(out, `import { readFile } from "node:fs/promises";`, "")
		}
		out = append(out, "const form = new FormData();")
		for _, f := range p.fields {
			if f.file {
				out = append(out, "form.append("+js(f.name)+", new Blob([await readFile("+jsonString(f.path)+")]), "+jsonString(filepath.Base(f.path))+");")
			} else {
				out = append(out, "form.append("+js(f.name)+", "+js(f.value)+");")
			}
		}
		opts = append(opts, "  body: form,")
	}
	out = append(out, "const response = await fetch("+js(p.url)+", {")
	out = append(out, opts...)
	out = append(out, "});", "console.log(response.status, await response.text());")
	return strings.Join(out, "\n"), nil
}

func exportPython(p exportPlan) (string, error) {
	py := func(v value) string { return code(v, func(n string) string { return `os.environ["` + n + `"]` }) }
	out := exportNotes(p, "#", "%s")
	if len(p.envs) > 0 {
		out = append(out, "import os")
	}
	out = append(out, "import requests", "", "response = requests.request(", "    "+py(lit(p.method))+",", "    "+py(p.url)+",")
	if len(p.headers) > 0 || len(p.contentType) > 0 {
		out = append(out, "    headers={")
		if len(p.contentType) > 0 {
			out = append(out, `        "Content-Type": `+py(p.contentType)+",")
		}
		for _, h := range p.headers {
			out = append(out, "        "+py(h.name)+": "+py(h.value)+",")
		}
		out = append(out, "    },")
	}
	if len(p.body) > 0 {
		out = append(out, "    data="+py(p.body)+",")
	}
	var data, files []string
	for _, f := range p.fields {
		if f.file {
			files = append(files, "        "+py(f.name)+": open("+jsonString(f.path)+", \"rb\"),")
		} else {
			data = append(data, "        "+py(f.name)+": "+py(f.value)+",")
		}
	}
	if len(data) > 0 {
		out = append(append(append(out, "    data={"), data...), "    },")
	}
	if len(files) > 0 {
		out = append(append(append(out, "    files={"), files...), "    },")
	}
	return strings.Join(append(out, ")", "print(response.status_code, response.text)"), "\n"), nil
}

func withNotes(notes []string, body string) string {
	if len(notes) == 0 {
		return body
	}
	return strings.Join(notes, "\n") + "\n" + body
}

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
