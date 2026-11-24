package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

const swagger2 = `{
  "swagger": "2.0",
  "info": {"title": "Example API", "version": "1.0"},
  "host": "localhost:8080",
  "basePath": "/v1",
  "parameters": {"Expand": {"name": "expand", "in": "query", "type": "boolean", "description": "Expand nested"}},
  "paths": {
    "/items/{id}": {
      "parameters": [{"name": "id", "in": "path", "required": true, "type": "string", "example": "42"}],
      "get": {
        "summary": "Get item",
        "tags": ["items"],
        "parameters": [{"$ref": "#/parameters/Expand"}],
        "security": [{"ApiKeyAuth": []}],
        "responses": {
          "200": {"description": "OK", "schema": {"$ref": "#/definitions/Item"}},
          "404": {"description": "Not found"}
        }
      },
      "post": {
        "summary": "Update item",
        "deprecated": true,
        "parameters": [
          {"name": "X-Store", "in": "header", "type": "string"},
          {"name": "request", "in": "body", "schema": {"$ref": "#/definitions/Item"}}
        ],
        "responses": {"200": {"description": "OK"}}
      }
    },
    "/upload": {
      "post": {
        "consumes": ["multipart/form-data"],
        "parameters": [
          {"name": "file", "in": "formData", "type": "file", "required": true, "example": "/proc/self/environ"},
          {"name": "note", "in": "formData", "type": "string"}
        ],
        "responses": {"201": {"description": "Created"}}
      }
    }
  },
  "securityDefinitions": {"ApiKeyAuth": {"type": "apiKey", "name": "X-API-Key", "in": "header"}},
  "definitions": {
    "Item": {
      "type": "object",
      "required": ["id"],
      "properties": {
        "id": {"type": "integer", "example": 7},
        "name": {"type": "string", "format": "email"},
        "status": {"type": "string", "enum": ["active", "archived"]},
        "tags": {"type": "array", "items": {"type": "string"}},
        "parent": {"$ref": "#/definitions/Item"}
      }
    }
  }
}`

const openapi3 = `{
  "openapi": "3.1.0",
  "info": {"title": "Pets", "version": "2"},
  "servers": [{"url": "https://{region}.example.com/v2/", "variables": {"region": {"default": "eu"}}}, {"url": "http://localhost:9000"}],
  "security": [{"bearer": []}],
  "components": {
    "securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}},
    "requestBodies": {"PetBody": {"content": {"application/json": {"schema": {"$ref": "common.json#/Pet"}}}}}
  },
  "paths": {
    "/pets": {
      "post": {"requestBody": {"$ref": "#/components/requestBodies/PetBody"},
        "responses": {"201": {"description": "Created", "content": {"application/json": {"example": {"id": 1}}}}}},
      "put": {"requestBody": {"content": {"application/x-www-form-urlencoded": {"schema": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string", "default": "rex"}, "age": {"type": "integer"}}}}}},
        "responses": {"default": {"description": "Error"}}},
      "delete": {"security": [], "responses": {"204": {"description": "Gone"}}}
    }
  }
}`

const commonJSON = `{"Pet": {"allOf": [
  {"type": "object", "properties": {"name": {"type": ["string", "null"]}}},
  {"type": "object", "properties": {"age": {"type": "number"}}}
]}}`

const swaggerYAML = `swagger: "2.0"
info:
  title: YAML API # comment
  version: 1.0
paths:
  /ping:
    get:
      summary: Ping
      responses:
        "200":
          description: pong
`

func TestParse(t *testing.T) {
	fsys := fstest.MapFS{
		"api/swagger.json":            {Data: []byte(swagger2)},
		"api/pets/openapi.json":       {Data: []byte(openapi3)},
		"api/pets/common.json":        {Data: []byte(commonJSON)},
		"yaml/swagger.yaml":           {Data: []byte(swaggerYAML)},
		"twin/swagger.json":           {Data: []byte(`{"swagger":"2.0","info":{"title":"JSON twin"}}`)},
		"twin/swagger.yaml":           {Data: []byte("not: [valid")},
		"broken/swagger.json":         {Data: []byte(`{"swagger": `)},
		"badref/openapi.json":         {Data: []byte(`{"openapi":"3.0.0","paths":{"/x":{"get":{"parameters":[{"$ref":"../../etc/passwd#/x"}]}}}}`)},
		"conf/openapi-lint.json":      {Data: []byte(`{"rules": {}}`)},
		"node_modules/x/swagger.json": {Data: []byte(swagger2)},
		"fanout/openapi.json":         {Data: []byte(fanoutSpec(12, 20))},
		"hidden/openapi.json":         {Data: []byte(`{"openapi":"3.0.0","paths":{"/x":{"get":{"parameters":[{"$ref":".env#/x"}]}}}}`)},
		"hidden/.env":                 {Data: []byte(`{"x":{"name":"leak","in":"query"}}`)},
		"longpath/openapi.json":       {Data: []byte(`{"openapi":"3.0.0","paths":{"/` + strings.Repeat("?", maxPathKey) + `":{}}}`)},
		"ctlpath/openapi.json":        {Data: []byte(`{"openapi":"3.0.0","paths":{"/a\n":{}}}`)},
		"bidipath/openapi.json":       {Data: []byte(`{"openapi":"3.0.0","paths":{"/b\u202e":{}}}`)},
		"zwsppath/openapi.json":       {Data: []byte(`{"openapi":"3.0.0","paths":{"/items\u200b":{}}}`)},
	}
	specs, _, ops, err := scanSpecs(fsys)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]bikasend.Spec{}
	for _, s := range specs {
		byPath[s.Path] = s
	}
	var paths []string
	for p := range byPath {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	want := []string{"api/pets/openapi.json", "api/swagger.json", "badref/openapi.json", "bidipath/openapi.json", "broken/swagger.json", "ctlpath/openapi.json", "fanout/openapi.json", "hidden/openapi.json", "longpath/openapi.json", "twin/swagger.json", "yaml/swagger.yaml", "zwsppath/openapi.json"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("specs = %v, want %v", paths, want)
	}
	if byPath["broken/swagger.json"].Error == "" || byPath["badref/openapi.json"].Error == "" || byPath["hidden/openapi.json"].Error == "" {
		t.Error("broken spec, ref outside the folder and ref to a hidden file must report an error")
	}
	for _, name := range []string{"longpath/openapi.json", "ctlpath/openapi.json", "bidipath/openapi.json", "zwsppath/openapi.json"} {
		if e := byPath[name].Error; e == "" || len(e) > 200 || strings.Contains(e, "/a\n") || strings.ContainsAny(e, "\u202e\u200b") {
			t.Errorf("%s: a long or control-character path must fail with a short, clean error, got %d bytes: %q", name, len(e), e)
		}
	}
	if e := byPath["longpath/openapi.json"].Error; !strings.Contains(e, `"…`) {
		t.Errorf("a cut path must end in an ellipsis: %q", e)
	}
	if e := byPath["bidipath/openapi.json"].Error; !strings.Contains(e, "U+202E at byte 2") {
		t.Errorf("the error must name the refused character: %q", e)
	}
	if f := byPath["fanout/openapi.json"]; f.Error != "" || len(f.Operations) != 1 || f.Operations[0].Body == "" || len(f.Operations[0].Body) > 1<<20 {
		t.Errorf("fan-out spec must get a bounded example: error %q, body %d bytes", f.Error, len(f.Operations[0].Body))
	}
	if h := byPath["fanout/openapi.json"].Operations[0].BodyHints; len(h) == 0 || len(h) > maxHints {
		t.Errorf("fan-out spec must get bounded body hints: %d", len(h))
	}
	if y := byPath["yaml/swagger.yaml"]; y.Title != "YAML API" || len(y.Operations) != 1 || y.Operations[0].Summary != "Ping" {
		t.Errorf("yaml spec = %+v", y)
	}

	s2 := byPath["api/swagger.json"]
	if !reflect.DeepEqual(s2.Servers, []string{"http://localhost:8080/v1"}) {
		t.Errorf("swagger 2 servers = %v", s2.Servers)
	}
	get := s2.Operations[0]
	if get.Method != http.MethodGet || get.Tag != "items" || get.Headers != "X-API-Key: {{X-API-Key}}" || get.ID != "api/swagger.json GET /items/{id}" {
		t.Errorf("get = %+v", get)
	}
	wantParams := []bikasend.Param{
		{Name: "id", In: "path", Required: true, Example: "42"},
		{Name: "expand", In: "query", Description: "Expand nested", Example: "false"},
	}
	if !reflect.DeepEqual(get.Params, wantParams) {
		t.Errorf("params = %+v", get.Params)
	}
	if len(get.Responses) != 2 || get.Responses[0].Example == "" || get.Responses[1].Status != "404" {
		t.Errorf("responses = %+v", get.Responses)
	}
	post := s2.Operations[1]
	wantBody := "{\n  \"id\": 7,\n  \"name\": \"user@example.com\",\n  \"parent\": null,\n  \"status\": \"active\",\n  \"tags\": [\n    \"\"\n  ]\n}"
	if !post.Deprecated || post.BodyType != "json" || post.Body != wantBody || post.Headers != "Content-Type: application/json\nX-Store: " {
		t.Errorf("post = %+v\nbody %s", post, post.Body)
	}
	upload := s2.Operations[2]
	if upload.BodyType != "multipart" || len(upload.Form) != 2 || !upload.Form[0].File || upload.Form[0].Value != "" || !upload.Form[0].Enabled || upload.Form[1].Enabled {
		t.Errorf("upload = %+v", upload)
	}

	s3 := byPath["api/pets/openapi.json"]
	if !reflect.DeepEqual(s3.Servers, []string{"https://eu.example.com/v2", "http://localhost:9000"}) {
		t.Errorf("openapi servers = %v", s3.Servers)
	}
	var del, postPet, put bikasend.Operation
	for _, o := range s3.Operations {
		switch o.Method {
		case http.MethodDelete:
			del = o
		case http.MethodPost:
			postPet = o
		case http.MethodPut:
			put = o
		}
	}
	if postPet.Body != "{\n  \"age\": 0,\n  \"name\": \"\"\n}" || postPet.Headers != "Authorization: Bearer {{token}}\nContent-Type: application/json" {
		t.Errorf("external ref body = %q headers = %q", postPet.Body, postPet.Headers)
	}
	if postPet.Responses[0].Example != "{\n  \"id\": 1\n}" {
		t.Errorf("response example = %q", postPet.Responses[0].Example)
	}
	if put.BodyType != "form" || !reflect.DeepEqual(put.Form, []bikasend.Field{{Name: "age", Value: "0"}, {Name: "name", Value: "rex", Enabled: true}}) {
		t.Errorf("form = %+v", put.Form)
	}
	if put.Responses[0].Status != "default" || del.Headers != "" {
		t.Errorf("default response or empty security wrong: %+v %+v", put.Responses, del)
	}
	if _, ok := ops["api/swagger.json GET /items/{id}"]; !ok {
		t.Error("op details missing for validation")
	}
}

const hintSpec = `{
  "openapi": "3.0.0",
  "paths": {"/orders": {"post": {"requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Order"}}}}, "responses": {}}},
    "/ping": {"get": {"responses": {}}}},
  "components": {"schemas": {"Order": {"type": "object", "properties": {
    "status": {"type": "string", "enum": ["new", "paid"]},
    "rush": {"type": "boolean"},
    "note": {"type": "string"},
    "address": {"properties": {"city": {"type": "string"}, "zip": {"type": "string"}}},
    "items": {"type": "array", "items": {"allOf": [{"properties": {"sku": {"type": "string"}}}, {"properties": {"qty": {"type": "integer", "enum": [1, 2]}}}]}},
    "parent": {"$ref": "#/components/schemas/Order"}
  }}}}
}`

func TestBodyHints(t *testing.T) {
	spec, _, err := newDocs(fstest.MapFS{"openapi.json": {Data: []byte(hintSpec)}}).parse("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []bikasend.BodyHint{
		{Path: "", Kind: "object", Keys: []string{"address", "items", "note", "parent", "rush", "status"}, Enum: []string{}},
		{Path: "address", Kind: "object", Keys: []string{"city", "zip"}, Enum: []string{}},
		{Path: "items[]", Kind: "object", Keys: []string{"sku", "qty"}, Enum: []string{}},
		{Path: "items[].qty", Kind: "integer", Keys: []string{}, Enum: []string{"1", "2"}},
		{Path: "rush", Kind: "boolean", Keys: []string{}, Enum: []string{"true", "false"}},
		{Path: "status", Kind: "string", Keys: []string{}, Enum: []string{`"new"`, `"paid"`}},
	}
	if got := spec.Operations[0].BodyHints; !reflect.DeepEqual(got, want) {
		t.Errorf("hints =\n%+v\nwant\n%+v", got, want)
	}
	if got := spec.Operations[1].BodyHints; got == nil || len(got) != 0 {
		t.Errorf("an operation without a body must have an empty hint list, got %#v", got)
	}

	wide := map[string]any{}
	for i := range maxHints + 10 {
		wide[fmt.Sprintf("p%03d", i)] = map[string]any{"type": "boolean"}
	}
	if got := newDocs(nil).bodyHints("x.json", map[string]any{"properties": wide}); len(got) != maxHints {
		t.Errorf("hints past the cap: %d, want %d", len(got), maxHints)
	}

	enum := []any{strings.Repeat("x", maxHintEnum)}
	for i := range maxHintItems + 10 {
		enum = append(enum, i)
	}
	h := newDocs(nil).bodyHints("x.json", map[string]any{"type": "integer", "enum": enum})
	if len(h) != 1 || len(h[0].Enum) != maxHintItems-1 || slices.ContainsFunc(h[0].Enum, func(e string) bool { return len(e) > maxHintEnum }) {
		t.Errorf("enum hints must skip long values and stop at %d: %d", maxHintItems, len(h[0].Enum))
	}

	// A node with 100,000 keys spends the budget instead of being sorted on every visit.
	huge := map[string]any{}
	for i := range 100_000 {
		huge[strconv.Itoa(i)] = true
	}
	d := newDocs(nil)
	if got := d.bodyHints("x.json", map[string]any{"properties": huge}); len(got) != 0 || d.examplesLeft >= maxExampleTotal {
		t.Errorf("a huge node must spend the budget and give no hints: %d hints, %d left", len(got), d.examplesLeft)
	}
}

func TestScanRealFolder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "swagger.json"), []byte(swagger2), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(swagger2), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinked := os.Symlink(outside, filepath.Join(root, "api", "leak-swagger.json")) == nil

	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	specs, _, _, err := scanSpecs(r.FS())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		if s.Path == "api/leak-swagger.json" && symlinked && (s.Title != "" || s.Error == "") {
			t.Errorf("symlink out of the folder was read: %+v", s)
		}
	}
	if _, _, _, err := scanSpecs(os.DirFS(filepath.Join(root, "missing"))); !errors.Is(err, bikasend.ErrScan) {
		t.Errorf("missing root: err = %v, want ErrScan", err)
	}
}

// fanoutSpec has levels schemas where each has width properties that ref the next: width^levels nodes.
func fanoutSpec(levels, width int) string {
	schemas := map[string]any{fmt.Sprintf("L%d", levels): map[string]any{"type": "string"}}
	for i := range levels {
		props := map[string]any{}
		for j := range width {
			props[fmt.Sprintf("p%d", j)] = map[string]any{"$ref": fmt.Sprintf("#/components/schemas/L%d", i+1)}
		}
		schemas[fmt.Sprintf("L%d", i)] = map[string]any{"type": "object", "properties": props}
	}
	ref := map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/L0"}}}
	doc := map[string]any{
		"openapi":    "3.0.0",
		"paths":      map[string]any{"/x": map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": ref}, "responses": map[string]any{"200": map[string]any{"description": "ok", "content": ref}}}}},
		"components": map[string]any{"schemas": schemas},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func TestLoadLimits(t *testing.T) {
	d := newDocs(fstest.MapFS{"a.json": {Data: []byte(`{"a": 1}`)}, "b.json": {Data: []byte(`{"b": 2}`)}})
	d.scanLeft = 8
	if _, err := d.load("a.json"); err != nil {
		t.Fatalf("first file within the folder budget: %v", err)
	}
	if _, err := d.load("b.json"); err == nil {
		t.Error("a file past the folder budget must fail")
	}
	if _, err := readCapped(fstest.MapFS{"big.json": {Data: make([]byte, 11)}}, "big.json", 10); err == nil {
		t.Error("readCapped must refuse a file over the limit")
	}
	// preload decodes in parallel but reserves the budget in file order: the same files fit every time.
	files := fstest.MapFS{"broken.json": {Data: []byte(`{"a": `)}}
	var names []string
	for i := range 8 {
		n := fmt.Sprintf("f%d.json", i)
		files[n] = &fstest.MapFile{Data: []byte(`{"n": 1234}`)}
		names = append(names, n)
	}
	for range 20 {
		p := newDocs(files)
		p.scanLeft = 11*4 + 6
		p.preload(append([]string{"broken.json"}, names...))
		for i, n := range names {
			if _, cached := p.cache[n]; cached != (i < 4) {
				t.Fatalf("%s cached = %v; budgets must go in file order", n, cached)
			}
		}
		left := p.scanLeft
		if _, err := p.load("broken.json"); err == nil || p.scanLeft != left {
			t.Fatalf("a broken file keeps its error and is not read again: %v, budget %d → %d", err, left, p.scanLeft)
		}
		if _, err := p.load("f7.json"); err == nil || !strings.Contains(err.Error(), "MB of specs") {
			t.Fatalf("a file past the folder budget: %v", err)
		}
	}
}

func FuzzParseJSON(f *testing.F) {
	for _, seed := range []string{swagger2, openapi3, `{"swagger":"2.0","paths":{"/a":{"$ref":"#"}}}`, `{"openapi":"3","paths":{"/a":{"get":{"responses":{"200":{"$ref":"#/paths/~1a/get/responses/200"}}}}}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		d := newDocs(fstest.MapFS{"openapi.json": {Data: []byte(doc)}})
		_, _, _ = d.parse("openapi.json")
	})
}
