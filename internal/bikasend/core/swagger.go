package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

const (
	maxSpec = 20 << 20
	maxScan = 200 << 20
	// Refs can fan out (20 properties, each a ref with 20 more); budgets stop exponential examples.
	maxExampleNodes = 2_000
	maxExampleTotal = 1_000_000
)

var methods = []string{"get", "post", "put", "patch", "delete", "head", "options"}

// docs loads spec files from one folder and follows $ref chains, including refs to other files.
type docs struct {
	fsys         fs.FS
	cache        map[string]any
	scanLeft     int64
	examplesLeft int
}

// opDetail keeps what response validation needs after the scan.
type opDetail struct {
	file      string
	responses map[string]any
}

func newDocs(fsys fs.FS) *docs {
	return &docs{fsys: fsys, cache: map[string]any{}, scanLeft: maxScan, examplesLeft: maxExampleTotal}
}

func (d *docs) load(name string) (any, error) {
	if v, ok := d.cache[name]; ok {
		return v, nil
	}
	info, err := fs.Stat(d.fsys, name)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSpec {
		return nil, fmt.Errorf("file is %d MB, limit is %d MB", info.Size()>>20, maxSpec>>20)
	}
	if info.Size() > d.scanLeft {
		return nil, fmt.Errorf("the folder has more than %d MB of specs", maxScan>>20)
	}
	data, err := readCapped(d.fsys, name, maxSpec)
	if err != nil {
		return nil, err
	}
	d.scanLeft -= int64(len(data))
	var v any
	if isYAML(name) {
		v, err = parseYAML(data)
	} else {
		err = json.Unmarshal(data, &v)
	}
	if err != nil {
		return nil, err
	}
	d.cache[name] = v
	return v, nil
}

// readCapped reads at most limit bytes, so a file that grows after Stat cannot pass the cap.
func readCapped(fsys fs.FS, name string, limit int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file is larger than %d MB", limit>>20)
	}
	return data, nil
}

// deref follows $ref until it reaches a node without one. It returns the node and its file.
func (d *docs) deref(file string, v any) (any, string, error) {
	for range 32 {
		r, ok := obj(v)["$ref"].(string)
		if !ok {
			return v, file, nil
		}
		target, ptr, _ := strings.Cut(r, "#")
		if target != "" {
			if strings.Contains(target, "://") {
				return nil, file, fmt.Errorf("remote $ref %q is not supported", r)
			}
			file = path.Join(path.Dir(file), target)
			if !fs.ValidPath(file) {
				return nil, file, fmt.Errorf("$ref %q points outside the folder", r)
			}
			if !refTarget(file) {
				return nil, file, fmt.Errorf("$ref %q must point to a visible .json, .yaml or .yml file", r)
			}
		}
		root, err := d.load(file)
		if err != nil {
			return nil, file, fmt.Errorf("$ref %q: %w", r, err)
		}
		if v, err = pointer(root, ptr); err != nil {
			return nil, file, fmt.Errorf("$ref %q: %w", r, err)
		}
	}
	return nil, file, errors.New("$ref chain is too long")
}

// refTarget keeps $ref away from hidden files such as .env and from non-spec files.
func refTarget(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(name))
	return ext == ".json" || ext == ".yaml" || ext == ".yml"
}

func pointer(root any, ptr string) (any, error) {
	ptr, err := url.PathUnescape(ptr)
	if err != nil {
		return nil, err
	}
	if ptr == "" || ptr == "/" {
		return root, nil
	}
	v := root
	for part := range strings.SplitSeq(strings.TrimPrefix(ptr, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch n := v.(type) {
		case map[string]any:
			next, ok := n[part]
			if !ok {
				return nil, fmt.Errorf("%q not found", part)
			}
			v = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(n) {
				return nil, fmt.Errorf("index %q out of range", part)
			}
			v = n[i]
		default:
			return nil, fmt.Errorf("%q not found", part)
		}
	}
	return v, nil
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case int:
		return strconv.Itoa(s)
	case bool:
		return strconv.FormatBool(s)
	}
	return ""
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func isSpecName(name string) bool {
	n := strings.ToLower(name)
	return (strings.HasSuffix(n, ".json") || isYAML(n)) && (strings.Contains(n, "swagger") || strings.Contains(n, "openapi"))
}

// scanSpecs walks fsys for swagger and openapi files. A YAML file with a JSON twin is skipped.
func scanSpecs(fsys fs.FS) ([]bikasend.Spec, *docs, map[string]opDetail, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == "." {
				return err
			}
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if isSpecName(d.Name()) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, errors.Join(bikasend.ErrScan, err)
	}

	d := newDocs(fsys)
	specs := []bikasend.Spec{}
	details := map[string]opDetail{}
	for _, f := range files {
		if isYAML(f) && slices.Contains(files, strings.TrimSuffix(strings.TrimSuffix(f, ".yaml"), ".yml")+".json") {
			continue
		}
		spec, ops, err := d.parse(f)
		if errors.Is(err, bikasend.ErrNotSpec) {
			continue
		}
		if err != nil {
			spec.Error = err.Error()
		}
		spec.Path = f
		maps.Copy(details, ops)
		specs = append(specs, spec)
	}
	return specs, d, details, nil
}

func (d *docs) parse(file string) (bikasend.Spec, map[string]opDetail, error) {
	spec := bikasend.Spec{Servers: []string{}, Operations: []bikasend.Operation{}}
	root, err := d.load(file)
	if err != nil {
		return spec, nil, errors.Join(bikasend.ErrInvalidSpec, err)
	}
	doc := obj(root)
	swagger2 := str(doc["swagger"]) != ""
	if !swagger2 && str(doc["openapi"]) == "" {
		return spec, nil, bikasend.ErrNotSpec
	}
	info := obj(doc["info"])
	spec.Title, spec.Version = str(info["title"]), str(info["version"])
	spec.Servers = servers(doc, swagger2)

	schemes := obj(doc["securityDefinitions"])
	if !swagger2 {
		schemes = obj(obj(doc["components"])["securitySchemes"])
	}

	paths := obj(doc["paths"])
	ops := map[string]opDetail{}
	for _, p := range sortedKeys(paths) {
		item, itemFile, err := d.deref(file, paths[p])
		if err != nil {
			return spec, ops, errors.Join(bikasend.ErrInvalidSpec, fmt.Errorf("%s: %w", p, err))
		}
		for _, method := range methods {
			op, ok := obj(item)[method].(map[string]any)
			if !ok {
				continue
			}
			security := list(doc["security"])
			if s, ok := op["security"]; ok {
				security = list(s)
			}
			o, detail, err := d.operation(itemFile, method, p, op, list(obj(item)["parameters"]), security, schemes, swagger2, doc)
			if err != nil {
				return spec, ops, errors.Join(bikasend.ErrInvalidSpec, fmt.Errorf("%s %s: %w", strings.ToUpper(method), p, err))
			}
			o.ID = file + " " + o.Method + " " + p
			ops[o.ID] = detail
			spec.Operations = append(spec.Operations, o)
		}
	}
	return spec, ops, nil
}

func servers(doc map[string]any, swagger2 bool) []string {
	out := []string{}
	if !swagger2 {
		for _, s := range list(doc["servers"]) {
			u := str(obj(s)["url"])
			for name, v := range obj(obj(s)["variables"]) {
				u = strings.ReplaceAll(u, "{"+name+"}", str(obj(v)["default"]))
			}
			if u != "" {
				out = append(out, strings.TrimRight(u, "/"))
			}
		}
		if len(out) == 0 {
			out = append(out, "http://localhost")
		}
		return out
	}
	host := str(doc["host"])
	if host == "" {
		host = "localhost"
	}
	schemes := list(doc["schemes"])
	// swag omits schemes; default to http.
	if len(schemes) == 0 {
		schemes = []any{"http"}
	}
	for _, s := range schemes {
		out = append(out, strings.TrimRight(str(s)+"://"+host+str(doc["basePath"]), "/"))
	}
	return out
}

func (d *docs) operation(file, method, p string, op map[string]any, shared, security []any, schemes map[string]any, swagger2 bool, doc map[string]any) (bikasend.Operation, opDetail, error) {
	out := bikasend.Operation{
		Method:      strings.ToUpper(method),
		Path:        p,
		Summary:     str(op["summary"]),
		Description: str(op["description"]),
		Deprecated:  op["deprecated"] == true,
		Params:      []bikasend.Param{},
		Form:        []bikasend.Field{},
		Responses:   []bikasend.ResponseDoc{},
		BodyType:    "none",
	}
	if tags := list(op["tags"]); len(tags) > 0 {
		out.Tag = str(tags[0])
	}

	params := map[string]map[string]any{}
	var order []string
	for _, raw := range slices.Concat(shared, list(op["parameters"])) {
		pv, _, err := d.deref(file, raw)
		if err != nil {
			return out, opDetail{}, err
		}
		pm := obj(pv)
		key := str(pm["in"]) + ":" + str(pm["name"])
		if _, seen := params[key]; !seen {
			order = append(order, key)
		}
		params[key] = pm
	}

	var headers []string
	var bodySchema any
	consumes := list(op["consumes"])
	if len(consumes) == 0 {
		consumes = list(doc["consumes"])
	}
	for _, key := range order {
		pm := params[key]
		schema := pm
		if s, ok := pm["schema"]; ok {
			schema = obj(s)
		}
		param := bikasend.Param{
			Name:        str(pm["name"]),
			In:          str(pm["in"]),
			Description: str(pm["description"]),
			Required:    pm["required"] == true || str(pm["in"]) == "path",
			Example:     d.paramExample(file, pm, schema),
		}
		switch param.In {
		case "path", "query":
			out.Params = append(out.Params, param)
		case "header":
			headers = append(headers, param.Name+": "+param.Example)
		case "body":
			bodySchema, out.BodyType = pm["schema"], "json"
		case "formData":
			field := bikasend.Field{Name: param.Name, Value: param.Example, File: str(pm["type"]) == "file", Enabled: param.Required}
			// A spec must never choose a local file to upload.
			if field.File {
				field.Value = ""
			}
			out.Form = append(out.Form, field)
			out.BodyType = "form"
			if slices.Contains(consumes, any("multipart/form-data")) || str(pm["type"]) == "file" {
				out.BodyType = "multipart"
			}
		}
	}

	bodyFile := file
	if rb, ok := op["requestBody"]; ok {
		rbv, rbFile, err := d.deref(file, rb)
		if err != nil {
			return out, opDetail{}, err
		}
		if err := d.requestBody(&out, rbFile, obj(rbv), &bodySchema); err != nil {
			return out, opDetail{}, err
		}
		bodyFile = rbFile
	}

	for _, req := range security {
		for name := range obj(req) {
			sv, _, err := d.deref(file, schemes[name])
			if err != nil {
				continue
			}
			s := obj(sv)
			switch typ, scheme := str(s["type"]), strings.ToLower(str(s["scheme"])); {
			case typ == "apiKey" && str(s["in"]) == "header":
				headers = append(headers, str(s["name"])+": {{"+str(s["name"])+"}}")
			case typ == "apiKey" && str(s["in"]) == "query":
				out.Params = append(out.Params, bikasend.Param{Name: str(s["name"]), In: "query", Required: true, Example: "{{" + str(s["name"]) + "}}"})
			case typ == "basic" || scheme == "basic":
				headers = append(headers, "Authorization: Basic {{basicAuth}}")
			case typ == "http" || typ == "oauth2" || typ == "openIdConnect":
				headers = append(headers, "Authorization: Bearer {{token}}")
			}
		}
	}

	switch out.BodyType {
	case "json":
		headers = append(headers, "Content-Type: application/json")
		if out.Body == "" && bodySchema != nil {
			if b, err := json.MarshalIndent(d.sample(bodyFile, bodySchema), "", "  "); err == nil {
				out.Body = string(b)
			}
		}
	case "xml":
		headers = append(headers, "Content-Type: application/xml")
	case "text":
		headers = append(headers, "Content-Type: text/plain")
	}
	slices.Sort(headers)
	out.Headers = strings.Join(slices.Compact(headers), "\n")

	responses := obj(op["responses"])
	codes := sortedKeys(responses)
	if i := slices.Index(codes, "default"); i >= 0 {
		codes = append(slices.Delete(codes, i, i+1), "default")
	}
	detail := opDetail{file: file, responses: map[string]any{}}
	for _, c := range codes {
		rv, rFile, err := d.deref(file, responses[c])
		if err != nil {
			return out, opDetail{}, err
		}
		r := obj(rv)
		schema, example := responseSchema(r, swagger2)
		rd := bikasend.ResponseDoc{Status: c, Description: str(r["description"])}
		if example == nil && schema != nil {
			example = d.sample(rFile, schema)
		}
		if example != nil {
			if b, err := json.MarshalIndent(example, "", "  "); err == nil {
				rd.Example = string(b)
			}
		}
		out.Responses = append(out.Responses, rd)
		detail.responses[c] = responseRef{schema: schema, file: rFile}
	}
	return out, detail, nil
}

type responseRef struct {
	schema any
	file   string
}

func (d *docs) requestBody(out *bikasend.Operation, file string, rb map[string]any, bodySchema *any) error {
	content := obj(rb["content"])
	types := sortedKeys(content)
	for _, ct := range types {
		if isJSONType(ct) {
			media := obj(content[ct])
			out.BodyType = "json"
			*bodySchema = media["schema"]
			if ex := mediaExample(media); ex != nil {
				if b, err := json.MarshalIndent(ex, "", "  "); err == nil {
					out.Body = string(b)
				}
			}
			return nil
		}
	}
	for _, kind := range []struct{ ct, body string }{{"multipart/form-data", "multipart"}, {"application/x-www-form-urlencoded", "form"}} {
		media, ok := content[kind.ct]
		if !ok {
			continue
		}
		out.BodyType = kind.body
		sv, sFile, err := d.deref(file, obj(media)["schema"])
		if err != nil {
			return err
		}
		props := obj(obj(sv)["properties"])
		required := list(obj(sv)["required"])
		for _, n := range sortedKeys(props) {
			pv, _, _ := d.deref(sFile, props[n])
			pm := obj(pv)
			isFile := str(pm["format"]) == "binary" || str(pm["format"]) == "base64"
			value := ""
			if !isFile {
				value = str(d.sample(sFile, pm))
			}
			out.Form = append(out.Form, bikasend.Field{Name: n, Value: value, File: isFile, Enabled: slices.Contains(required, any(n))})
		}
		return nil
	}
	for _, ct := range types {
		switch {
		case strings.Contains(ct, "xml"):
			out.BodyType = "xml"
			return nil
		case strings.HasPrefix(ct, "text/"):
			out.BodyType = "text"
			return nil
		}
	}
	return nil
}

func isJSONType(ct string) bool {
	ct, _, _ = strings.Cut(ct, ";")
	ct = strings.TrimSpace(ct)
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}

func mediaExample(media map[string]any) any {
	if ex, ok := media["example"]; ok {
		return ex
	}
	examples := obj(media["examples"])
	for _, n := range sortedKeys(examples) {
		if v, ok := obj(examples[n])["value"]; ok {
			return v
		}
	}
	return nil
}

func responseSchema(r map[string]any, swagger2 bool) (schema, example any) {
	if swagger2 {
		return r["schema"], obj(r["examples"])["application/json"]
	}
	content := obj(r["content"])
	for _, ct := range sortedKeys(content) {
		if isJSONType(ct) {
			media := obj(content[ct])
			return media["schema"], mediaExample(media)
		}
	}
	return nil, nil
}

func (d *docs) paramExample(file string, p, schema map[string]any) string {
	if ex, ok := p["example"]; ok {
		return str(ex)
	}
	switch v := d.sample(file, schema).(type) {
	case map[string]any, []any, nil:
		return ""
	default:
		return str(v)
	}
}

// sample builds an example for v within the per-example and per-scan node budgets.
func (d *docs) sample(file string, v any) any {
	budget := min(maxExampleNodes, d.examplesLeft)
	left := budget
	out := d.example(file, v, nil, &left)
	d.examplesLeft -= budget - left
	return out
}

// example builds a sample value; a ref already on the path is a cycle and becomes null.
func (d *docs) example(file string, v any, seen []string, left *int) any {
	s := obj(v)
	if s == nil || len(seen) > 16 || *left <= 0 {
		return nil
	}
	*left--
	if r, ok := s["$ref"].(string); ok {
		key := file + r
		if slices.Contains(seen, key) {
			return nil
		}
		target, tFile, err := d.deref(file, s)
		if err != nil {
			return nil
		}
		return d.example(tFile, target, append(seen, key), left)
	}
	switch {
	case s["example"] != nil:
		return s["example"]
	case s["default"] != nil:
		return s["default"]
	case len(list(s["enum"])) > 0:
		return list(s["enum"])[0]
	case len(list(s["examples"])) > 0:
		return list(s["examples"])[0]
	case len(list(s["allOf"])) > 0:
		merged := map[string]any{}
		for _, part := range list(s["allOf"]) {
			maps.Copy(merged, obj(d.example(file, part, seen, left)))
		}
		return merged
	case len(list(s["oneOf"])) > 0:
		return d.example(file, list(s["oneOf"])[0], seen, left)
	case len(list(s["anyOf"])) > 0:
		return d.example(file, list(s["anyOf"])[0], seen, left)
	}

	switch schemaType(s) {
	case "array":
		if item := d.example(file, s["items"], seen, left); item != nil {
			return []any{item}
		}
		return []any{}
	case "string":
		switch str(s["format"]) {
		case "date-time":
			return "2024-01-01T00:00:00Z"
		case "date":
			return "2024-01-01"
		case "uuid":
			return "00000000-0000-0000-0000-000000000000"
		case "email":
			return "user@example.com"
		case "uri", "url":
			return "https://example.com"
		}
		return ""
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "object":
		out := map[string]any{}
		for name, p := range obj(s["properties"]) {
			out[name] = d.example(file, p, seen, left)
		}
		return out
	case "":
		if props := obj(s["properties"]); props != nil {
			out := map[string]any{}
			for name, p := range props {
				out[name] = d.example(file, p, seen, left)
			}
			return out
		}
	}
	return nil
}

// OpenAPI 3.1 allows a list of types, for example ["string", "null"].
func schemaType(s map[string]any) string {
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		for _, v := range t {
			if str(v) != "null" {
				return str(v)
			}
		}
	}
	return ""
}
