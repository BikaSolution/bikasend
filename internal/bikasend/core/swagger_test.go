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
	want := []string{"api/pets/openapi.json", "api/swagger.json", "badref/openapi.json", "broken/swagger.json", "fanout/openapi.json", "hidden/openapi.json", "twin/swagger.json", "yaml/swagger.yaml"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("specs = %v, want %v", paths, want)
	}
	if byPath["broken/swagger.json"].Error == "" || byPath["badref/openapi.json"].Error == "" || byPath["hidden/openapi.json"].Error == "" {
		t.Error("broken spec, ref outside the folder and ref to a hidden file must report an error")
	}
	if f := byPath["fanout/openapi.json"]; f.Error != "" || len(f.Operations) != 1 || f.Operations[0].Body == "" || len(f.Operations[0].Body) > 1<<20 {
		t.Errorf("fan-out spec must get a bounded example: error %q, body %d bytes", f.Error, len(f.Operations[0].Body))
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
