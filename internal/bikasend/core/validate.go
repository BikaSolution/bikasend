package core

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

const (
	maxIssues = 50
	// allOf and oneOf re-check the same value, so a schema can multiply the work per level.
	maxChecks = 100_000
)

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
	for _, key := range []string{"oneOf", "anyOf"} {
		options := list(s[key])
		if len(options) == 0 {
			continue
		}
		matched := false
		for _, o := range options {
			var tmp []string
			d.check(file, o, value, at, seen, &tmp, left)
			if len(tmp) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			report("matches none of the %s options", key)
		}
	}

	if value == nil {
		if s["nullable"] == true || allowsNull(s) || schemaType(s) == "" {
			return
		}
		report("is null, want %s", schemaType(s))
		return
	}

	if enum := list(s["enum"]); len(enum) > 0 && !slices.ContainsFunc(enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(value) }) {
		report("value %v is not one of %v", value, enum)
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
		for i, item := range items {
			d.check(file, s["items"], item, at+"["+strconv.Itoa(i)+"]", seen, issues, left)
		}
	case "integer":
		if f, ok := value.(float64); !ok || f != math.Trunc(f) {
			report("is %s, want integer", kind(value))
		}
	case "number", "string", "boolean":
		if kind(value) != want && (want != "number" || kind(value) != "integer") {
			report("is %s, want %s", kind(value), want)
		}
	}
}

func (d *docs) checkObject(file string, s, m map[string]any, at string, seen []string, issues *[]string, left *int, report func(string, ...any)) {
	props := obj(s["properties"])
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
