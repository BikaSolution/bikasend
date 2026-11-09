package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseYAML(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    any
		wantErr bool
	}{
		{
			name: "swag style document",
			in: `swagger: "2.0"
info:
  description: 'It''s a test'
  version: 1.0
  contact: {}
paths:
  /a/{id}:
    get:
      consumes:
      - application/json
      parameters:
        - name: id
          in: path
          required: true
          type: string
      responses:
        "200":
          description: OK
`,
			want: map[string]any{
				"swagger": "2.0",
				"info":    map[string]any{"description": "It's a test", "version": 1.0, "contact": map[string]any{}},
				"paths": map[string]any{"/a/{id}": map[string]any{"get": map[string]any{
					"consumes":   []any{"application/json"},
					"parameters": []any{map[string]any{"name": "id", "in": "path", "required": true, "type": "string"}},
					"responses":  map[string]any{"200": map[string]any{"description": "OK"}},
				}}},
			},
		},
		{
			name: "block scalars, flow collections, comments and continuation",
			in: `literal: |
  line one
  line two
folded: >-
  one
  two
flow: [a, "b, c", {k: 1}]
# a comment
plain: a long
  wrapped value # trailing comment
url: http://x.example/#frag
empty:
nothing: ~
`,
			want: map[string]any{
				"literal": "line one\nline two\n",
				"folded":  "one two",
				"flow":    []any{"a", "b, c", map[string]any{"k": 1.0}},
				"plain":   "a long wrapped value",
				"url":     "http://x.example/#frag",
				"empty":   nil,
				"nothing": nil,
			},
		},
		{
			name: "nested sequences",
			in:   "- - 1\n  - 2\n- x\n",
			want: []any{[]any{1.0, 2.0}, "x"},
		},
		{name: "tabs", in: "a:\n\tb: 1\n", wantErr: true},
		{name: "anchor", in: "a: &x 1\n", wantErr: true},
		{name: "duplicate key", in: "a: 1\na: 2\n", wantErr: true},
		{name: "two documents", in: "a: 1\n---\nb: 2\n", wantErr: true},
		{name: "bad indentation", in: "a:\n    b: 1\n  c: 2\n", wantErr: true},
		{name: "stray brace in flow sequence", in: "a: [}", wantErr: true},
		{name: "stray bracket in flow mapping", in: "a: {k: 1]", wantErr: true},
		{name: "missing comma in flow sequence", in: `a: ["x" "y"]`, wantErr: true},
		{name: "deep flow nesting", in: "a: " + strings.Repeat("[", 100_000), wantErr: true},
		{name: "deep sequence nesting", in: strings.Repeat("- ", 100_000) + "x\n", wantErr: true},
		{name: "deep mapping nesting", in: deepMapping(300), wantErr: true},
		{name: "flow nesting at the limit", in: "a: " + strings.Repeat("[", 255) + strings.Repeat("]", 255), want: map[string]any{"a": nested(255)}},
		{name: "many continuation lines", in: "a: x\n" + strings.Repeat("  y\n", 200_000), want: map[string]any{"a": "x" + strings.Repeat(" y", 200_000)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseYAML([]byte(tt.in))
			if tt.wantErr {
				if !errors.Is(err, errYAML) {
					t.Fatalf("err = %v, want a yaml error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func deepMapping(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(strings.Repeat(" ", i) + "k:\n")
	}
	return b.String()
}

func nested(n int) any {
	v := []any{}
	for range n - 1 {
		v = []any{v}
	}
	return v
}

func FuzzParseYAML(f *testing.F) {
	for _, seed := range []string{swaggerYAML, "a: [1, {b: 'c'}]\n", "- - x\n  - y\n", "k: |\n  a\n  b\n", `"q": "\u00e9"`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = parseYAML([]byte(in))
	})
}
