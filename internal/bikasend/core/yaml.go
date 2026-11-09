package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Block mappings, sequences, scalars, block scalars and one-line flow collections cover
// what swag and most OpenAPI tools write. Anchors, aliases, tags and multi-document files error.
// Upgrade path: a YAML module, if a real spec needs more.

var errYAML = errors.New("yaml")

// Real specs nest under 30 levels; the limit keeps hostile files from overflowing the stack.
const maxYAMLDepth = 256

type yamlLine struct {
	indent int
	text   string
	num    int
}

type yamlParser struct {
	lines []yamlLine
	pos   int
	depth int
}

func parseYAML(data []byte) (any, error) {
	p := &yamlParser{}
	for i, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimLeft(raw, " ")
		if strings.HasPrefix(trimmed, "\t") {
			return nil, fmt.Errorf("%w: line %d: tabs are not allowed for indentation", errYAML, i+1)
		}
		text := strings.TrimRight(stripComment(trimmed), " ")
		if text == "" {
			continue
		}
		if text == "---" && len(p.lines) == 0 {
			continue
		}
		if text == "---" || text == "..." {
			return nil, fmt.Errorf("%w: line %d: multiple documents are not supported", errYAML, i+1)
		}
		p.lines = append(p.lines, yamlLine{indent: len(raw) - len(trimmed), text: strings.TrimRight(text, " "), num: i + 1})
	}
	if len(p.lines) == 0 {
		return nil, nil
	}
	v, err := p.block(p.lines[0].indent)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, p.errorf("unexpected indentation")
	}
	return v, nil
}

// stripComment removes a trailing "# comment" that is not inside quotes.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
		case c == '"' || c == '\'':
			if i == 0 || strings.ContainsRune(" :[{,-", rune(s[i-1])) {
				quote = c
			}
		case c == '#' && (i == 0 || s[i-1] == ' '):
			return s[:i]
		}
	}
	return s
}

func (p *yamlParser) errorf(format string, args ...any) error {
	line := 0
	if p.pos < len(p.lines) {
		line = p.lines[p.pos].num
	} else if len(p.lines) > 0 {
		line = p.lines[len(p.lines)-1].num
	}
	return fmt.Errorf("%w: line %d: %s", errYAML, line, fmt.Sprintf(format, args...))
}

func isSeqItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

func (p *yamlParser) block(indent int) (any, error) {
	if p.pos >= len(p.lines) {
		return nil, nil
	}
	if isSeqItem(p.lines[p.pos].text) {
		return p.sequence(indent)
	}
	return p.mapping(indent)
}

func (p *yamlParser) sequence(indent int) (any, error) {
	if p.depth++; p.depth > maxYAMLDepth {
		return nil, p.errorf("nesting is deeper than %d levels", maxYAMLDepth)
	}
	defer func() { p.depth-- }()
	out := []any{}
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent < indent || (line.indent == indent && !isSeqItem(line.text)) {
			break
		}
		if line.indent > indent {
			return nil, p.errorf("bad sequence indentation")
		}
		rest := strings.TrimLeft(strings.TrimPrefix(line.text, "-"), " ")
		if rest == "" {
			p.pos++
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				v, err := p.block(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			} else {
				out = append(out, nil)
			}
			continue
		}
		childIndent := indent + (len(line.text) - len(rest))
		if _, _, ok := splitKey(rest); ok && !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, "{") {
			// "- key: value" starts a mapping at the column after "- ".
			p.lines[p.pos] = yamlLine{indent: childIndent, text: rest, num: line.num}
			v, err := p.mapping(childIndent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		if isSeqItem(rest) {
			p.lines[p.pos] = yamlLine{indent: childIndent, text: rest, num: line.num}
			v, err := p.sequence(childIndent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		p.pos++
		v, err := p.inlineValue(rest, indent)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (p *yamlParser) mapping(indent int) (any, error) {
	if p.depth++; p.depth > maxYAMLDepth {
		return nil, p.errorf("nesting is deeper than %d levels", maxYAMLDepth)
	}
	defer func() { p.depth-- }()
	out := map[string]any{}
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, p.errorf("bad mapping indentation")
		}
		if isSeqItem(line.text) {
			break
		}
		key, rest, ok := splitKey(line.text)
		if !ok {
			return nil, p.errorf("expected \"key: value\"")
		}
		if _, dup := out[key]; dup {
			return nil, p.errorf("duplicate key %q", key)
		}
		p.pos++
		switch {
		case rest == "":
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				v, err := p.block(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				out[key] = v
			} else if p.pos < len(p.lines) && p.lines[p.pos].indent == indent && isSeqItem(p.lines[p.pos].text) {
				v, err := p.sequence(indent)
				if err != nil {
					return nil, err
				}
				out[key] = v
			} else {
				out[key] = nil
			}
		case rest[0] == '|' || rest[0] == '>':
			out[key] = p.blockScalar(rest, indent)
		default:
			v, err := p.inlineValue(rest, indent)
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
	}
	return out, nil
}

// splitKey finds "key: rest" or "key:" outside quotes and brackets.
func splitKey(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' || s[0] == '\'' {
		end := closingQuote(s)
		if end < 0 || end+1 >= len(s) || s[end+1] != ':' || (end+2 < len(s) && s[end+2] != ' ') {
			return "", "", false
		}
		key, err := unquote(s[:end+1])
		if err != nil {
			return "", "", false
		}
		return key, strings.TrimSpace(s[end+2:]), true
	}
	if s[0] == '[' || s[0] == '{' {
		return "", "", false
	}
	for i := range len(s) {
		if s[i] == ':' && (i+1 == len(s) || s[i+1] == ' ') {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

func closingQuote(s string) int {
	q := s[0]
	for i := 1; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q:
			if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			return i
		}
	}
	return -1
}

func (p *yamlParser) blockScalar(header string, indent int) any {
	folded := header[0] == '>'
	chomp := strings.TrimLeft(header[1:], "0123456789")
	var parts []string
	blockIndent := -1
	for p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
		line := p.lines[p.pos]
		if blockIndent < 0 {
			blockIndent = line.indent
		}
		parts = append(parts, strings.Repeat(" ", max(0, line.indent-blockIndent))+line.text)
		p.pos++
	}
	sep := "\n"
	if folded {
		sep = " "
	}
	s := strings.Join(parts, sep)
	if chomp != "-" && s != "" {
		s += "\n"
	}
	return s
}

// inlineValue parses a scalar or a one-line flow collection, plus plain-scalar continuation lines.
func (p *yamlParser) inlineValue(s string, indent int) (any, error) {
	if s[0] == '&' || s[0] == '*' || s[0] == '!' {
		return nil, p.errorf("anchors, aliases and tags are not supported")
	}
	if s[0] == '[' || s[0] == '{' {
		v, rest, err := flowValue(s, p.depth)
		if err != nil {
			return nil, p.errorf("%v", err)
		}
		if strings.TrimSpace(rest) != "" {
			return nil, p.errorf("unexpected text after flow collection")
		}
		return v, nil
	}
	if s[0] == '"' || s[0] == '\'' {
		end := closingQuote(s)
		if end < 0 {
			return nil, p.errorf("unterminated quote")
		}
		return unquote(s[:end+1])
	}
	parts := []string{s}
	for p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
		parts = append(parts, p.lines[p.pos].text)
		p.pos++
	}
	return plainScalar(strings.Join(parts, " ")), nil
}

func unquote(s string) (string, error) {
	if s[0] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	}
	v, err := strconv.Unquote(strings.ReplaceAll(s, `\/`, "/"))
	if err != nil {
		return "", fmt.Errorf("%w: bad quoted string %s", errYAML, s)
	}
	return v, nil
}

func plainScalar(s string) any {
	switch s {
	case "null", "Null", "NULL", "~":
		return nil
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(n)
	}
	if strings.ContainsAny(s, ".eE") {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return s
}

func flowValue(s string, depth int) (any, string, error) {
	if depth > maxYAMLDepth {
		return nil, "", fmt.Errorf("nesting is deeper than %d levels", maxYAMLDepth)
	}
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return nil, "", errors.New("empty flow value")
	}
	switch s[0] {
	case '[':
		out := []any{}
		s = strings.TrimLeft(s[1:], " ")
		for {
			if s == "" {
				return nil, "", errors.New("unterminated [")
			}
			if s[0] == ']' {
				return out, s[1:], nil
			}
			v, rest, err := flowValue(s, depth+1)
			if err != nil {
				return nil, "", err
			}
			out = append(out, v)
			s = strings.TrimLeft(rest, " ")
			switch {
			case strings.HasPrefix(s, ","):
				s = strings.TrimLeft(s[1:], " ")
			case !strings.HasPrefix(s, "]"):
				return nil, "", errors.New("expected , or ] in flow sequence")
			}
		}
	case '{':
		out := map[string]any{}
		s = strings.TrimLeft(s[1:], " ")
		for {
			if s == "" {
				return nil, "", errors.New("unterminated {")
			}
			if s[0] == '}' {
				return out, s[1:], nil
			}
			k, rest, err := flowScalar(s, ":")
			if err != nil {
				return nil, "", err
			}
			rest = strings.TrimLeft(rest, " ")
			if !strings.HasPrefix(rest, ":") {
				return nil, "", errors.New("expected : in flow mapping")
			}
			v, rest, err := flowValue(rest[1:], depth+1)
			if err != nil {
				return nil, "", err
			}
			out[fmt.Sprint(k)] = v
			s = strings.TrimLeft(rest, " ")
			switch {
			case strings.HasPrefix(s, ","):
				s = strings.TrimLeft(s[1:], " ")
			case !strings.HasPrefix(s, "}"):
				return nil, "", errors.New("expected , or } in flow mapping")
			}
		}
	}
	return flowScalar(s, "")
}

func flowScalar(s, extraStops string) (any, string, error) {
	if s[0] == '"' || s[0] == '\'' {
		end := closingQuote(s)
		if end < 0 {
			return nil, "", errors.New("unterminated quote")
		}
		v, err := unquote(s[:end+1])
		return v, s[end+1:], err
	}
	end := strings.IndexAny(s, ",]}"+extraStops)
	if end < 0 {
		end = len(s)
	}
	return plainScalar(strings.TrimSpace(s[:end])), s[end:], nil
}
