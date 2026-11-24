package core

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestValidateResponse(t *testing.T) {
	d := newDocs(fstest.MapFS{"swagger.json": {Data: []byte(swagger2)}})
	_, ops, err := d.parse("swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	op := ops["swagger.json GET /items/{id}"]

	tests := []struct {
		name   string
		status int
		ct     string
		body   string
		want   []string
	}{
		{name: "matches", status: 200, ct: "application/json", body: `{"id": 1, "name": "a@example.com", "status": "active", "tags": ["x"], "parent": {"id": 2}}`, want: []string{}},
		{
			name: "type, enum, required and nested problems", status: 200, ct: "application/json; charset=utf-8",
			body: `{"id": 1.5, "status": "gone", "tags": [3], "parent": {}}`,
			want: []string{
				"$.id: is number, want integer",
				"$.parent: missing required field \"id\"",
				"$.status: value gone is not one of [active archived]",
				"$.tags[0]: is integer, want string",
			},
		},
		{name: "documented status without schema", status: 404, ct: "application/json", body: `{}`, want: []string{}},
		{name: "undocumented status", status: 500, ct: "application/json", body: `{}`, want: []string{"status 500 is not documented for this operation"}},
		{name: "not json content", status: 200, ct: "text/plain", body: `hello`, want: []string{}},
		{name: "invalid json", status: 200, ct: "application/json", body: `{`, want: []string{"the body is not valid JSON"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.validateResponse(op, tt.status, tt.ct, []byte(tt.body))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}

	strict := newDocs(fstest.MapFS{"o.json": {Data: []byte(`{"openapi":"3.0.0","paths":{"/x":{"get":{"responses":{"2XX":{"description":"ok","content":{"application/json":{"schema":{"type":"object","additionalProperties":false,"nullable":true,"properties":{"a":{"type":"string","nullable":true}}}}}}}}}}}`)}})
	_, sops, err := strict.parse("o.json")
	if err != nil {
		t.Fatal(err)
	}
	got := strict.validateResponse(sops["o.json GET /x"], 201, "application/json", []byte(`{"a": null, "b": 1}`))
	if !reflect.DeepEqual(got, []string{`$: unexpected field "b"`}) {
		t.Errorf("2XX, nullable and additionalProperties: %q", got)
	}

	rules := newDocs(fstest.MapFS{"r.json": {Data: []byte(rulesSpec)}})
	_, rops, err := rules.parse("r.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, body string
		want       []string
	}{
		{
			name: "formats, ranges, counts, oneOf, not and const that hold",
			body: `{"day": "2026-11-24", "at": "2026-11-24T10:00:00Z", "id": "123e4567-e89b-12d3-a456-426614174000", "ip": "10.0.0.1", "code": "ABC", "qty": 4, "ratio": 0.5, "old": 1, "tags": ["a"], "kind": "item", "shape": "", "notnum": "x", "bad": "anything"}`,
			want: []string{},
		},
		{
			name: "every rule broken once",
			body: `{"day": "2026-13-01", "at": "yesterday", "id": "nope", "ip": "::1", "code": "abcd", "qty": 3, "ratio": 1, "old": 0, "tags": ["a", "a", "b", "c"], "kind": "x", "shape": "s", "notnum": 5, "bad": "anything"}`,
			want: []string{
				"$.at: is not a valid date-time",
				"$.code: has 4 characters, want at most 3",
				`$.code: does not match the pattern "^[A-Z]{3}$"`,
				"$.day: is not a valid date",
				"$.id: is not a valid uuid",
				"$.ip: is not a valid ipv4",
				"$.kind: value x is not item",
				"$.notnum: matches a schema it must not match",
				"$.old: is 0, want more than 0",
				"$.qty: is 3, want a multiple of 2",
				"$.ratio: is 1, want less than 1",
				"$.shape: matches 2 of the oneOf options, want exactly one",
				"$.tags: has 4 items, want at most 3",
				"$.tags: has duplicate items",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := rules.validateResponse(rops["r.json GET /v"], 200, "application/json", []byte(tt.body))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}

	chain := newDocs(fstest.MapFS{"o.json": {Data: []byte(allOfChain(30))}})
	_, cops, err := chain.parse("o.json")
	if err != nil {
		t.Fatal(err)
	}
	got = chain.validateResponse(cops["o.json GET /x"], 200, "application/json", []byte(`{}`))
	if len(got) != 1 || !strings.HasPrefix(got[0], "stopped after") {
		t.Errorf("allOf fan-out must stop at the check budget: %q", got)
	}
}

// rulesSpec uses every keyword the response check knows beyond type, enum and required. "bad" has
// an invalid pattern, which is skipped rather than reported.
const rulesSpec = `{"openapi": "3.1.0", "paths": {"/v": {"get": {"responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {
  "type": "object", "minProperties": 1, "maxProperties": 20, "properties": {
    "day": {"type": "string", "format": "date"},
    "at": {"type": "string", "format": "date-time"},
    "id": {"type": "string", "format": "uuid"},
    "ip": {"type": "string", "format": "ipv4"},
    "code": {"type": "string", "pattern": "^[A-Z]{3}$", "minLength": 3, "maxLength": 3},
    "qty": {"type": "integer", "minimum": 1, "maximum": 10, "multipleOf": 2},
    "ratio": {"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 1},
    "old": {"type": "number", "minimum": 0, "exclusiveMinimum": true},
    "tags": {"type": "array", "minItems": 1, "maxItems": 3, "uniqueItems": true, "items": {"type": "string"}},
    "kind": {"const": "item"},
    "shape": {"oneOf": [{"type": "string"}, {"type": "string", "minLength": 1}]},
    "notnum": {"not": {"type": "number"}},
    "bad": {"type": "string", "pattern": "("}
  }}}}}}}}}}`

// allOfChain has n schemas where each is allOf two refs to the next: 2^n checks of one value.
func allOfChain(n int) string {
	var defs []string
	for i := range n {
		next := fmt.Sprintf(`{"$ref":"#/components/schemas/A%d"}`, i+1)
		defs = append(defs, fmt.Sprintf(`"A%d":{"allOf":[%s,%s]}`, i, next, next))
	}
	defs = append(defs, fmt.Sprintf(`"A%d":{"type":"object"}`, n))
	return `{"openapi":"3.0.0","paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/A0"}}}}}}}},"components":{"schemas":{` + strings.Join(defs, ",") + `}}}`
}
