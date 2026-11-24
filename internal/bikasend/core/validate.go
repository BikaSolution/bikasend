package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxIssues = 50
	// allOf and oneOf re-check the same value, so a schema can multiply the work per level.
	maxChecks = 100_000
	// Go's regexp is linear, but compiling a huge pattern still costs; skip those.
	maxPattern = 1_000
	// uniqueItems compares encoded items; past this many items it is not checked.
	maxUniqueItems = 2_000
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateResponse checks a JSON body against the documented response for status.
// It returns readable issues; an empty list means the body matches.
func (d *docs) validateResponse(op opDetail, status int, contentType string, body []byte) []string {
	if len(op.responses) == 0 {
		return []string{}
	}
	code := strconv.Itoa(status)
	ref, ok := op.responses[code].(responseRef)
	if !ok {
		ref, ok = op.responses[code[:1]+"XX"].(responseRef)
	}
	if !ok {
		ref, ok = op.responses[code[:1]+"xx"].(responseRef)
	}
	if !ok {
		ref, ok = op.responses["default"].(responseRef)
	}
	if !ok {
		return []string{fmt.Sprintf("status %d is not documented for this operation", status)}
	}
	if ref.schema == nil || !isJSONType(contentType) {
		return []string{}
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return []string{"the body is not valid JSON"}
	}
	issues := []string{}
	left := maxChecks
	d.check(ref.file, ref.schema, value, "$", nil, &issues, &left)
	if left <= 0 {
		issues = append(issues, fmt.Sprintf("stopped after %d checks: the schema or body is too large to check", maxChecks))
	}
	return issues
}

func (d *docs) check(file string, schemaNode, value any, at string, seen []string, issues *[]string, left *int) {
	if len(*issues) >= maxIssues || len(seen) > 32 || *left <= 0 {
		return
	}
	*left--
	s := obj(schemaNode)
	if s == nil {
		return
	}
	if r, ok := s["$ref"].(string); ok {
		key := file + r + "@" + at
		if slices.Contains(seen, key) {
			return
		}
		target, tFile, err := d.deref(file, s)
		if err != nil {
			return
		}
		d.check(tFile, target, value, at, append(seen, key), issues, left)
		return
	}
	report := func(format string, args ...any) {
		if len(*issues) < maxIssues {
			*issues = append(*issues, at+": "+fmt.Sprintf(format, args...))
		}
	}

	for _, part := range list(s["allOf"]) {
		d.check(file, part, value, at, seen, issues, left)
	}
	matches := func(schema any) bool {
		var tmp []string
		d.check(file, schema, value, at, seen, &tmp, left)
		return len(tmp) == 0
	}
	if options := list(s["anyOf"]); len(options) > 0 && !slices.ContainsFunc(options, matches) {
		report("matches none of the anyOf options")
	}
	if options := list(s["oneOf"]); len(options) > 0 {
		n := 0
		for _, o := range options {
			if matches(o) {
				n++
			}
		}
		switch {
		case n == 0:
			report("matches none of the oneOf options")
		case n > 1:
			report("matches %d of the oneOf options, want exactly one", n)
		}
	}
	if not, ok := s["not"]; ok && obj(not) != nil && matches(not) {
		report("matches a schema it must not match")
	}
	// Work that grows with the value's size costs one check per KB.
	spend := func(n int) bool {
		*left -= n >> 10
		return *left > 0
	}
	// text is a value as compared and reported; objects and arrays are encoded and paid for.
	text := func(v any) (string, bool) {
		switch v.(type) {
		case map[string]any, []any:
			b, err := json.Marshal(v)
			return string(b), err == nil && spend(len(b))
		}
		return fmt.Sprint(v), true
	}
	if c, ok := s["const"]; ok {
		want, ok1 := text(c)
		got, ok2 := text(value)
		if ok1 && ok2 && got != want {
			report("value %s is not %s", brief(got), brief(want))
		}
	}

	if value == nil {
		if s["nullable"] == true || allowsNull(s) || schemaType(s) == "" {
			return
		}
		report("is null, want %s", schemaType(s))
		return
	}

	if enum := list(s["enum"]); len(enum) > 0 && spend(len(enum)) {
		got, ok := text(value)
		if ok && !slices.ContainsFunc(enum, func(e any) bool { t, _ := text(e); return t == got }) {
			report("value %s is not one of %s", brief(got), brief(fmt.Sprint(enum[:min(len(enum), 20)])))
		}
	}

	switch want := schemaType(s); want {
	case "object":
		m, ok := value.(map[string]any)
		if !ok {
			report("is %s, want object", kind(value))
			return
		}
		d.checkObject(file, s, m, at, seen, issues, left, report)
	case "":
		if m, ok := value.(map[string]any); ok && s["properties"] != nil {
			d.checkObject(file, s, m, at, seen, issues, left, report)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			report("is %s, want array", kind(value))
			return
		}
		checkCount(s, "minItems", "maxItems", len(items), "items", report)
		if s["uniqueItems"] == true && len(items) <= maxUniqueItems && !unique(items, spend) {
			report("has duplicate items")
		}
		for i, item := range items {
			d.check(file, s["items"], item, at+"["+strconv.Itoa(i)+"]", seen, issues, left)
		}
	case "integer":
		if f, ok := value.(float64); !ok || f != math.Trunc(f) {
			report("is %s, want integer", kind(value))
			return
		}
		checkNumber(s, value.(float64), report)
	case "number":
		f, ok := value.(float64)
		if !ok {
			report("is %s, want number", kind(value))
			return
		}
		checkNumber(s, f, report)
	case "string":
		str, ok := value.(string)
		if !ok {
			report("is %s, want string", kind(value))
			return
		}
		if spend(len(str)) {
			d.checkString(s, str, report)
		}
	case "boolean":
		if kind(value) != want {
			report("is %s, want %s", kind(value), want)
		}
	}
}

func checkCount(s map[string]any, minKey, maxKey string, n int, what string, report func(string, ...any)) {
	if lo, ok := s[minKey].(float64); ok && float64(n) < lo {
		report("has %d %s, want at least %v", n, what, lo)
	}
	if hi, ok := s[maxKey].(float64); ok && float64(n) > hi {
		report("has %d %s, want at most %v", n, what, hi)
	}
}

// checkNumber reads exclusiveMinimum both ways: a flag in OpenAPI 3.0, a number in 3.1.
func checkNumber(s map[string]any, f float64, report func(string, ...any)) {
	if lo, ok := s["minimum"].(float64); ok {
		if s["exclusiveMinimum"] == true && f <= lo {
			report("is %v, want more than %v", f, lo)
		} else if f < lo {
			report("is %v, want at least %v", f, lo)
		}
	}
	if hi, ok := s["maximum"].(float64); ok {
		if s["exclusiveMaximum"] == true && f >= hi {
			report("is %v, want less than %v", f, hi)
		} else if f > hi {
			report("is %v, want at most %v", f, hi)
		}
	}
	if lo, ok := s["exclusiveMinimum"].(float64); ok && f <= lo {
		report("is %v, want more than %v", f, lo)
	}
	if hi, ok := s["exclusiveMaximum"].(float64); ok && f >= hi {
		report("is %v, want less than %v", f, hi)
	}
	if m, ok := s["multipleOf"].(float64); ok && m > 0 {
		if q := f / m; math.Abs(q-math.Round(q)) > 1e-9 {
			report("is %v, want a multiple of %v", f, m)
		}
	}
}

func (d *docs) checkString(s map[string]any, v string, report func(string, ...any)) {
	checkCount(s, "minLength", "maxLength", utf8.RuneCountInString(v), "characters", report)
	if p := str(s["pattern"]); p != "" {
		if re := d.pattern(p); re != nil && !re.MatchString(v) {
			report("does not match the pattern %s", quoteStart(p))
		}
	}
	if f := str(s["format"]); f != "" && !validFormat(f, v) {
		report("is not a valid %s", f)
	}
}

// pattern compiles and caches a schema's pattern; an invalid or huge one is skipped, not reported.
func (d *docs) pattern(p string) *regexp.Regexp {
	d.mu.Lock()
	defer d.mu.Unlock()
	if re, ok := d.patterns[p]; ok {
		return re
	}
	var re *regexp.Regexp
	if len(p) <= maxPattern {
		re, _ = regexp.Compile(p)
	}
	if d.patterns == nil {
		d.patterns = map[string]*regexp.Regexp{}
	}
	d.patterns[p] = re
	return re
}

// validFormat knows the common formats; an unknown format passes.
func validFormat(format, v string) bool {
	switch format {
	case "date":
		_, err := time.Parse(time.DateOnly, v)
		return err == nil
	case "date-time":
		_, err := time.Parse(time.RFC3339, v)
		return err == nil
	case "time":
		_, err := time.Parse("15:04:05Z07:00", v)
		if err != nil {
			_, err = time.Parse(time.TimeOnly, v)
		}
		return err == nil
	case "uuid":
		return uuidPattern.MatchString(v)
	case "email":
		a, err := mail.ParseAddress(v)
		return err == nil && a.Address == v
	case "uri":
		u, err := url.Parse(v)
		return err == nil && u.Scheme != ""
	case "ipv4":
		a, err := netip.ParseAddr(v)
		return err == nil && a.Is4()
	case "ipv6":
		a, err := netip.ParseAddr(v)
		return err == nil && a.Is6()
	case "byte":
		_, err := base64.StdEncoding.DecodeString(v)
		return err == nil
	}
	return true
}

// brief keeps a value short enough for an issue line.
func brief(s string) string {
	if utf8.RuneCountInString(s) <= 60 {
		return s
	}
	return quoteStart(s)
}

// unique compares encoded items; encoding/json sorts object keys. Out of budget counts as unique.
func unique(items []any, spend func(int) bool) bool {
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		b, err := json.Marshal(it)
		if err != nil || !spend(len(b)) {
			return true
		}
		if seen[string(b)] {
			return false
		}
		seen[string(b)] = true
	}
	return true
}

func (d *docs) checkObject(file string, s, m map[string]any, at string, seen []string, issues *[]string, left *int, report func(string, ...any)) {
	props := obj(s["properties"])
	checkCount(s, "minProperties", "maxProperties", len(m), "fields", report)
	for _, r := range list(s["required"]) {
		if _, ok := m[str(r)]; !ok {
			report("missing required field %q", str(r))
		}
	}
	for _, name := range sortedKeys(m) {
		if p, ok := props[name]; ok {
			d.check(file, p, m[name], at+"."+name, seen, issues, left)
			continue
		}
		if s["additionalProperties"] == false {
			report("unexpected field %q", name)
		} else if extra := obj(s["additionalProperties"]); extra != nil {
			d.check(file, extra, m[name], at+"."+name, seen, issues, left)
		}
	}
}

func allowsNull(s map[string]any) bool {
	return slices.ContainsFunc(list(s["type"]), func(t any) bool { return str(t) == "null" })
}

func kind(v any) string {
	switch t := v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if t == math.Trunc(t) {
			return "integer"
		}
		return "number"
	case nil:
		return "null"
	}
	return strings.ToLower(fmt.Sprintf("%T", v))
}
