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
		{name: "matches", status: 200, ct: "application/json", body: `{"id": 1, "name": "a", "status": "active", "tags": ["x"], "parent": {"id": 2}}`, want: []string{}},
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
