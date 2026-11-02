// Command gen reads openapi.yaml and writes the generated parts of package
// slidecold: the operation table, the models and the typed service methods.
//
// Run it with `go generate ./...` from the repository root.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

func main() {
	log.SetFlags(0)
	raw, err := os.ReadFile("openapi.yaml")
	if err != nil {
		log.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		log.Fatal(err)
	}
	g := &gen{spec: convert(&doc).(*omap), decls: map[string]*decl{}}
	g.run()
	for name, src := range map[string]*bytes.Buffer{
		"operations_gen.go": g.ops,
		"models_gen.go":     g.modelsFile(),
		"services_gen.go":   g.svc,
	} {
		out, err := format.Source(src.Bytes())
		if err != nil {
			_ = os.WriteFile(name, src.Bytes(), 0o644)
			log.Fatalf("%s: %v", name, err)
		}
		if err := os.WriteFile(name, out, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

// ---------- ordered YAML ----------

type omap struct {
	keys []string
	vals map[string]any
}

func (m *omap) get(k string) any {
	if m == nil {
		return nil
	}
	return m.vals[k]
}
func (m *omap) obj(k string) *omap { o, _ := m.get(k).(*omap); return o }
func (m *omap) str(k string) string {
	s, _ := m.get(k).(string)
	return s
}
func (m *omap) list(k string) []any { l, _ := m.get(k).([]any); return l }

func (m *omap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(m.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func convert(n *yaml.Node) any {
	switch n.Kind {
	case yaml.DocumentNode:
		return convert(n.Content[0])
	case yaml.AliasNode:
		return convert(n.Alias)
	case yaml.MappingNode:
		m := &omap{vals: map[string]any{}}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			m.keys = append(m.keys, k)
			m.vals[k] = convert(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		l := []any{}
		for _, c := range n.Content {
			l = append(l, convert(c))
		}
		return l
	default:
		var v any
		if err := n.Decode(&v); err != nil {
			log.Fatal(err)
		}
		return v
	}
}

// ---------- naming ----------

var initialisms = map[string]string{
	"id": "ID", "ids": "IDs", "ok": "OK", "url": "URL", "ip": "IP", "http": "HTTP", "api": "API", "csrf": "CSRF",
	"2fa": "2FA", "json": "JSON", "csv": "CSV", "uri": "URI",
}

// words splits camelCase, snake_case and SCREAMING_CASE into lower-case words.
// Digits start a word after a letter ("account2fa" -> account, 2fa).
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && len(cur) > 0 {
			prev := rs[i-1]
			switch {
			case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
				flush()
			case unicode.IsDigit(r) && unicode.IsLetter(prev):
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func title(w string) string {
	if v, ok := initialisms[w]; ok {
		return v
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

func pascalWords(ws []string) string {
	var b strings.Builder
	for _, w := range ws {
		b.WriteString(title(w))
	}
	return b.String()
}

func pascal(s string) string { return pascalWords(words(s)) }

func lowerCamel(s string) string {
	ws := words(s)
	return ws[0] + pascalWords(ws[1:])
}

func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return s[:len(s)-1]
	}
	return s
}

// ---------- generator ----------

type field struct {
	name, json, typ, doc string
	omit                 bool
}

type decl struct {
	name, doc string
	enum      []string // string enum when non-nil
	fields    []field
	embeds    []string
	csv       bool // gets a CSV field filled from text/csv responses
}

type gen struct {
	spec   *omap
	decls  map[string]*decl
	order  []string
	inputs map[string]bool // component schemas reachable from request bodies
	ops    *bytes.Buffer
	svc    *bytes.Buffer
}

func (g *gen) resolve(ref string) *omap {
	cur := any(g.spec)
	for _, p := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		cur = cur.(*omap).get(p)
	}
	return cur.(*omap)
}

func refName(ref string) string { return ref[strings.LastIndex(ref, "/")+1:] }

// deref returns a deep copy of v with every $ref replaced by its target.
func (g *gen) deref(v any) any {
	switch t := v.(type) {
	case *omap:
		if r := t.str("$ref"); r != "" {
			return g.deref(g.resolve(r))
		}
		m := &omap{vals: map[string]any{}}
		for _, k := range t.keys {
			m.keys = append(m.keys, k)
			m.vals[k] = g.deref(t.vals[k])
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = g.deref(e)
		}
		return out
	}
	return v
}

func (g *gen) collectRefs(v any, into map[string]bool) {
	switch t := v.(type) {
	case *omap:
		if r := t.str("$ref"); strings.HasPrefix(r, "#/components/schemas/") {
			n := refName(r)
			if !into[n] {
				into[n] = true
				g.collectRefs(g.resolve(r), into)
			}
		}
		for _, k := range t.keys {
			g.collectRefs(t.vals[k], into)
		}
	case []any:
		for _, e := range t {
			g.collectRefs(e, into)
		}
	}
}

func (g *gen) addDecl(d *decl) {
	if _, dup := g.decls[d.name]; dup {
		log.Fatalf("duplicate type %s", d.name)
	}
	g.decls[d.name] = d
	g.order = append(g.order, d.name)
}

// isObject reports whether a schema (after refs) produces a struct.
func (g *gen) isObject(s *omap) bool {
	if r := s.str("$ref"); r != "" {
		return g.isObject(g.resolve(r))
	}
	if s.get("allOf") != nil || s.obj("properties") != nil {
		return true
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		alts := nonNull(s.list(k))
		if len(alts) > 0 {
			for _, a := range alts {
				if !g.isObject(a) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func nonNull(l []any) []*omap {
	var out []*omap
	for _, e := range l {
		m := e.(*omap)
		if m.get("type") == "null" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// schemaType returns the Go type for a schema. ctx names any struct that has
// to be declared for it. nullable reports a null alternative.
func (g *gen) schemaType(s *omap, ctx string, input bool) (typ string, nullable bool) {
	if s == nil {
		return "json.RawMessage", false
	}
	if r := s.str("$ref"); r != "" {
		name := refName(r)
		g.component(name)
		return name, false
	}
	for _, k := range []string{"anyOf", "oneOf"} {
		if l := s.list(k); l != nil {
			alts := nonNull(l)
			nullable = len(alts) < len(l)
			if len(alts) == 1 {
				t, _ := g.schemaType(alts[0], ctx, input)
				return t, nullable
			}
			if g.isObject(s) {
				g.structDecl(ctx, s, input)
				return ctx, nullable
			}
			if allStrings(g, alts) {
				return "string", nullable
			}
			return "any", nullable
		}
	}
	if s.get("allOf") != nil || s.obj("properties") != nil {
		g.structDecl(ctx, s, input)
		return ctx, false
	}
	typ = "json.RawMessage"
	switch t := s.get("type").(type) {
	case string:
		typ = g.scalar(t, s, ctx, input)
	case []any:
		var rest []string
		for _, e := range t {
			if e == "null" {
				nullable = true
			} else {
				rest = append(rest, e.(string))
			}
		}
		if len(rest) == 1 {
			typ = g.scalar(rest[0], s, ctx, input)
		} else {
			typ = "any"
		}
	}
	return typ, nullable
}

func allStrings(g *gen, alts []*omap) bool {
	for _, a := range alts {
		if r := a.str("$ref"); r != "" {
			a = g.resolve(r)
		}
		if a.get("type") != "string" {
			return false
		}
	}
	return true
}

func (g *gen) scalar(t string, s *omap, ctx string, input bool) string {
	switch t {
	case "string":
		if s.str("format") == "date-time" {
			return "time.Time"
		}
		return "string"
	case "integer":
		return "int"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		it, nullable := g.schemaType(s.obj("items"), ctx, input)
		if nullable && !strings.HasPrefix(it, "[]") && !strings.HasPrefix(it, "map[") {
			it = "*" + it
		}
		return "[]" + it
	case "object":
		if ap, ok := s.get("additionalProperties").(*omap); ok {
			vt, _ := g.schemaType(ap, ctx+"Value", input)
			return "map[string]" + vt
		}
		return "map[string]any"
	}
	return "any"
}

// component declares a named schema from components/schemas once.
func (g *gen) component(name string) {
	if _, ok := g.decls[name]; ok {
		return
	}
	s := g.resolve("#/components/schemas/" + name)
	if enum := s.list("enum"); enum != nil && s.get("type") == "string" {
		d := &decl{name: name, doc: s.str("description"), enum: []string{}}
		for _, e := range enum {
			d.enum = append(d.enum, e.(string))
		}
		g.addDecl(d)
		return
	}
	if g.isObject(s) {
		g.structDecl(name, s, g.inputs[name])
		return
	}
	log.Fatalf("component %s: unsupported schema", name)
}

// props flattens allOf/oneOf/anyOf parts into one ordered property list.
// required holds properties required in every alternative.
func (g *gen) props(s *omap) (keys []string, ps map[string]*omap, required map[string]bool, doc string) {
	ps = map[string]*omap{}
	required = map[string]bool{}
	if r := s.str("$ref"); r != "" {
		return g.props(g.resolve(r))
	}
	doc = s.str("description")
	add := func(k []string, p map[string]*omap) {
		for _, key := range k {
			if _, seen := ps[key]; !seen {
				keys = append(keys, key)
			}
			ps[key] = p[key]
		}
	}
	if parts := s.list("allOf"); parts != nil {
		for _, p := range parts {
			k, pp, req, d := g.props(p.(*omap))
			add(k, pp)
			for r := range req {
				required[r] = true
			}
			if doc == "" {
				doc = d
			}
		}
	}
	for _, kind := range []string{"oneOf", "anyOf"} {
		alts := nonNull(s.list(kind))
		count := map[string]int{}
		for _, a := range alts {
			k, pp, req, _ := g.props(a)
			add(k, pp)
			for r := range req {
				count[r]++
			}
		}
		for r, n := range count {
			if n == len(alts) {
				required[r] = true
			}
		}
	}
	if p := s.obj("properties"); p != nil {
		for _, k := range p.keys {
			if _, seen := ps[k]; !seen {
				keys = append(keys, k)
			}
			ps[k] = p.obj(k)
		}
	}
	for _, r := range s.list("required") {
		required[r.(string)] = true
	}
	return keys, ps, required, doc
}

func (g *gen) structDecl(name string, s *omap, input bool) {
	if _, ok := g.decls[name]; ok {
		return
	}
	d := &decl{name: name}
	g.addDecl(d) // reserve the name before recursing
	keys, ps, required, doc := g.props(s)
	d.doc = doc
	for _, k := range keys {
		p := ps[k]
		fname := pascal(k)
		ctx := name + fname
		if p.get("type") == "array" {
			ctx = name + pascal(singular(k))
		}
		t, nullable := g.schemaType(p, ctx, input)
		ref := !strings.HasPrefix(t, "[]") && !strings.HasPrefix(t, "map[") && t != "any" && t != "json.RawMessage"
		f := field{name: fname, json: k, typ: t, doc: p.str("description"), omit: !required[k]}
		switch {
		case input && !required[k] && ref, nullable && ref:
			f.typ = "*" + t
		}
		d.fields = append(d.fields, f)
	}
}

// ---------- operations ----------

type op struct {
	id, method, path, tag, summary, description, scope string
	pathParams                                         []*omap
	queryParams                                        []*omap
	body                                               *omap // JSON body schema
	bodyRequired                                       bool
	fileField                                          string
	csv, csvOnly                                       bool
	goMethod                                           string
	actionWords                                        []string
	bodyType, paramsType, retType                      string
}

func (g *gen) run() {
	paths := g.spec.obj("paths")
	var ops []*op
	g.inputs = map[string]bool{}
	for _, p := range paths.keys {
		item := paths.obj(p)
		for _, m := range []string{"get", "post", "put", "patch", "delete"} {
			o := item.obj(m)
			if o == nil {
				continue
			}
			x := &op{
				id: o.str("operationId"), method: strings.ToUpper(m), path: p,
				summary: o.str("summary"), description: strings.TrimSpace(o.str("description")),
				scope: o.str("x-required-scope"),
			}
			if x.id == "" || x.scope == "" || len(o.list("tags")) != 1 {
				log.Fatalf("%s %s: operationId, one tag and x-required-scope are required", m, p)
			}
			x.tag = o.list("tags")[0].(string)
			for _, src := range [][]any{item.list("parameters"), o.list("parameters")} {
				for _, prm := range src {
					pm := prm.(*omap)
					if r := pm.str("$ref"); r != "" {
						pm = g.resolve(r)
					}
					if pm.str("in") == "path" {
						x.pathParams = append(x.pathParams, pm)
					} else if pm.str("in") == "query" {
						x.queryParams = append(x.queryParams, pm)
					}
				}
			}
			if rb := o.obj("requestBody"); rb != nil {
				x.bodyRequired, _ = rb.get("required").(bool)
				c := rb.obj("content")
				if j := c.obj("application/json"); j != nil {
					x.body = j.obj("schema")
					g.collectRefs(x.body, g.inputs)
				}
				if mp := c.obj("multipart/form-data"); mp != nil {
					props := mp.obj("schema").obj("properties")
					if len(props.keys) != 1 {
						log.Fatalf("%s: multipart body must have exactly one file field", x.id)
					}
					x.fileField = props.keys[0]
				}
			}
			ops = append(ops, x)
		}
	}
	if len(ops) == 0 {
		log.Fatal("no operations found")
	}

	seen := map[string]string{}
	for _, x := range ops {
		x.actionWords = actionWords(x.id, x.tag)
		x.goMethod = pascalWords(x.actionWords)
		key := x.tag + "." + x.goMethod
		if prev, dup := seen[key]; dup {
			log.Fatalf("%s and %s both map to %s", prev, x.id, key)
		}
		seen[key] = x.id
		g.types(x)
	}
	g.writeOps(ops)
	g.writeServices(ops)
}

// actionWords derives the method/action name: operationId words minus the
// resource noun, minus a leading "get" when something else remains.
func actionWords(id, tag string) []string {
	plural := strings.ToLower(tag)
	sing := singular(plural)
	var ws []string
	for _, w := range words(id) {
		if w != plural && w != sing {
			ws = append(ws, w)
		}
	}
	if len(ws) > 1 && ws[0] == "get" {
		ws = ws[1:]
	}
	return ws
}

func (g *gen) types(x *op) {
	base := pascal(x.id)
	if x.body != nil {
		name := base + "Request"
		if r := x.body.str("$ref"); r != "" {
			name = refName(r)
		}
		x.bodyType, _ = g.schemaType(x.body, name, true)
	}
	var optional []*omap
	for _, q := range x.queryParams {
		if req, _ := q.get("required").(bool); !req {
			optional = append(optional, q)
		}
	}
	if len(optional) > 0 {
		x.paramsType = base + "Params"
		d := &decl{name: x.paramsType, doc: x.paramsType + " holds the optional query parameters of " + x.id + "."}
		for _, q := range optional {
			t, _ := g.schemaType(q.obj("schema"), x.paramsType+pascal(q.str("name")), false)
			d.fields = append(d.fields, field{name: pascal(q.str("name")), json: q.str("name"), typ: t, doc: q.str("description")})
		}
		g.addDecl(d)
	}

	// Responses: every 2xx.
	resps := x.op2xx(g)
	var jsonTypes []string
	for _, r := range resps {
		c := r.resp.obj("content")
		if c.obj("text/csv") != nil {
			x.csv = true
		}
		if j := c.obj("application/json"); j != nil {
			ctx := base + "Response"
			if r.name != "" {
				ctx = r.name
			}
			s := j.obj("schema")
			if s.get("type") == "array" {
				t, _ := g.schemaType(s.obj("items"), base+"Item", false)
				jsonTypes = append(jsonTypes, "[]"+t)
			} else {
				t, _ := g.schemaType(s, ctx, false)
				jsonTypes = append(jsonTypes, t)
			}
		}
	}
	switch {
	case len(jsonTypes) == 0 && x.csv:
		x.csvOnly = true
		x.retType = "string"
	case len(jsonTypes) == 0:
		x.retType = ""
	case len(jsonTypes) == 1:
		x.retType = jsonTypes[0]
		if x.csv {
			d := g.decls[x.retType]
			if d == nil || d.enum != nil {
				log.Fatalf("%s: JSON+CSV response needs an object JSON schema", x.id)
			}
			d.csv = true
		}
	default:
		name := base + "Response"
		d := &decl{name: name, doc: name + " combines the possible success responses of " + x.id + "."}
		for _, t := range jsonTypes {
			if g.decls[t] == nil || g.decls[t].enum != nil {
				log.Fatalf("%s: several 2xx responses must all be objects", x.id)
			}
			d.embeds = append(d.embeds, t)
		}
		g.addDecl(d)
		x.retType = name
	}
}

type namedResp struct {
	name string
	resp *omap
}

func (x *op) op2xx(g *gen) []namedResp {
	o := g.spec.obj("paths").obj(x.path).obj(strings.ToLower(x.method))
	rs := o.obj("responses")
	var out []namedResp
	for _, code := range rs.keys {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		r := rs.obj(code)
		name := ""
		if ref := r.str("$ref"); ref != "" {
			name = refName(ref)
			r = g.resolve(ref)
		}
		out = append(out, namedResp{name, r})
	}
	return out
}

func (g *gen) modelsFile() *bytes.Buffer {
	var b bytes.Buffer
	b.WriteString(header + "package slidecold\n\nimport (\n\"encoding/json\"\n\"net/url\"\n\"strconv\"\n\"time\"\n)\n\n")
	b.WriteString("var (\n_ json.RawMessage\n_ time.Time\n_ = strconv.Itoa\n_ url.Values\n)\n\n")
	names := append([]string(nil), g.order...)
	sort.Strings(names)
	for _, n := range names {
		d := g.decls[n]
		doc := d.doc
		if !strings.HasPrefix(doc, d.name+" ") {
			doc = d.name + " is a SlideCold API type.\n" + doc
		}
		writeDoc(&b, doc, "")
		if d.enum != nil {
			fmt.Fprintf(&b, "type %s string\n\n// %s values.\nconst (\n", d.name, d.name)
			for _, e := range d.enum {
				fmt.Fprintf(&b, "%s%s %s = %q\n", d.name, pascal(e), d.name, e)
			}
			b.WriteString(")\n\n")
			continue
		}
		fmt.Fprintf(&b, "type %s struct {\n", d.name)
		for _, e := range d.embeds {
			fmt.Fprintf(&b, "%s\n", e)
		}
		for _, f := range d.fields {
			writeDoc(&b, f.doc, "")
			tag := f.json
			if f.omit {
				tag += ",omitempty"
			}
			fmt.Fprintf(&b, "%s %s `json:%q`\n", f.name, f.typ, tag)
		}
		if d.csv {
			b.WriteString("// CSV holds the body when the API answered with text/csv.\nCSV string `json:\"-\"`\n")
		}
		b.WriteString("}\n\n")
		if d.csv {
			fmt.Fprintf(&b, "func (r *%s) setCSV(s string) { r.CSV = s }\n\n", d.name)
		}
		if strings.HasSuffix(d.name, "Params") {
			fmt.Fprintf(&b, "func (p *%s) values() url.Values {\nv := url.Values{}\nif p == nil {\nreturn v\n}\n", d.name)
			for _, f := range d.fields {
				switch f.typ {
				case "int":
					fmt.Fprintf(&b, "if p.%s != 0 {\nv.Set(%q, strconv.Itoa(p.%s))\n}\n", f.name, f.json, f.name)
				default:
					fmt.Fprintf(&b, "if p.%s != \"\" {\nv.Set(%q, string(p.%s))\n}\n", f.name, f.json, f.name)
				}
			}
			b.WriteString("return v\n}\n\n")
		}
	}
	return &b
}

func writeDoc(b *bytes.Buffer, doc, indent string) {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return
	}
	for _, line := range strings.Split(doc, "\n") {
		fmt.Fprintf(b, "%s// %s\n", indent, strings.TrimRight(line, " "))
	}
}

const header = "// Code generated by internal/gen from openapi.yaml. DO NOT EDIT.\n\n"

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		log.Fatal(err)
	}
	return strconv.Quote(string(b))
}

func (g *gen) writeOps(ops []*op) {
	var b bytes.Buffer
	b.WriteString(header + "package slidecold\n\nimport \"encoding/json\"\n\n")
	b.WriteString("// Operations lists every SlideCold API operation, in spec order.\nvar Operations = []*Operation{\n")
	for _, x := range ops {
		fmt.Fprintf(&b, "op%s,\n", pascal(x.id))
	}
	b.WriteString("}\n\n")
	for _, x := range ops {
		fmt.Fprintf(&b, "var op%s = &Operation{\n", pascal(x.id))
		fmt.Fprintf(&b, "ID: %q,\nTag: %q,\nResource: %q,\nAction: %q,\nGoMethod: %q,\nToolName: %q,\n",
			x.id, x.tag, strings.ToLower(x.tag), strings.Join(x.actionWords, "-"), x.goMethod, strings.Join(words(x.id), "_"))
		fmt.Fprintf(&b, "HTTPMethod: %q,\nPath: %q,\nSummary: %q,\nDescription: %q,\nScope: %q,\n",
			x.method, x.path, x.summary, x.description, x.scope)
		if len(x.pathParams) > 0 {
			b.WriteString("PathParams: []string{")
			for _, p := range x.pathParams {
				fmt.Fprintf(&b, "%q,", p.str("name"))
			}
			b.WriteString("},\n")
		}
		if len(x.queryParams) > 0 {
			b.WriteString("QueryParams: []Param{\n")
			for _, q := range x.queryParams {
				req, _ := q.get("required").(bool)
				schema := g.deref(q.obj("schema")).(*omap)
				if d := q.str("description"); d != "" && schema.get("description") == nil {
					schema.keys = append(schema.keys, "description")
					schema.vals["description"] = d
				}
				fmt.Fprintf(&b, "{Name: %q, Required: %v, Description: %q, Schema: json.RawMessage(%s)},\n",
					q.str("name"), req, q.str("description"), jsonString(schema))
			}
			b.WriteString("},\n")
		}
		if x.body != nil {
			fmt.Fprintf(&b, "BodySchema: json.RawMessage(%s),\n", jsonString(g.deref(x.body)))
		}
		if x.bodyRequired {
			b.WriteString("BodyRequired: true,\n")
		}
		if x.fileField != "" {
			fmt.Fprintf(&b, "FileField: %q,\n", x.fileField)
		}
		if x.csv {
			b.WriteString("CSV: true,\n")
		}
		if x.method == "DELETE" || strings.HasPrefix(x.id, "send") {
			b.WriteString("Destructive: true,\n")
		}
		b.WriteString("}\n\n")
	}
	g.ops = &b
}

func (g *gen) writeServices(ops []*op) {
	var b bytes.Buffer
	b.WriteString(header + "package slidecold\n\nimport (\n\"context\"\n\"encoding/json\"\n\"io\"\n\"net/url\"\n)\n\nvar (\n_ io.Reader\n_ json.RawMessage\n)\n\n")
	var tags []string
	byTag := map[string][]*op{}
	for _, x := range ops {
		if byTag[x.tag] == nil {
			tags = append(tags, x.tag)
		}
		byTag[x.tag] = append(byTag[x.tag], x)
	}
	b.WriteString("// Client is a SlideCold API client. Create one with NewClient; it is safe for concurrent use.\ntype Client struct {\n")
	for _, t := range tags {
		fmt.Fprintf(&b, "%s *%sService\n", pascal(t), pascal(t))
	}
	b.WriteString("\ncore\n}\n\nfunc (c *Client) initServices() {\n")
	for _, t := range tags {
		fmt.Fprintf(&b, "c.%s = &%sService{c: c}\n", pascal(t), pascal(t))
	}
	b.WriteString("}\n\n")
	for _, t := range tags {
		desc := ""
		for _, tg := range g.spec.list("tags") {
			if tg.(*omap).str("name") == t {
				desc = tg.(*omap).str("description")
			}
		}
		fmt.Fprintf(&b, "// %sService groups the %s operations. %s\ntype %sService struct{ c *Client }\n\n", pascal(t), t, desc, pascal(t))
		for _, x := range byTag[t] {
			g.writeMethod(&b, x, false)
			if x.fileField != "" && x.body != nil {
				g.writeMethod(&b, x, true)
			}
		}
	}
	g.svc = &b
}

func (g *gen) writeMethod(b *bytes.Buffer, x *op, fileVariant bool) {
	name := x.goMethod
	multipart := x.fileField != "" && (x.body == nil || fileVariant)
	if fileVariant {
		name += "File"
	}
	sig := []string{"ctx context.Context"}
	var setPath, setQuery []string
	for _, p := range x.pathParams {
		v := lowerCamel(p.str("name"))
		sig = append(sig, v+" string")
		setPath = append(setPath, fmt.Sprintf("%q: %s", p.str("name"), v))
	}
	for _, q := range x.queryParams {
		if req, _ := q.get("required").(bool); req {
			v := lowerCamel(q.str("name"))
			sig = append(sig, v+" string")
			setQuery = append(setQuery, fmt.Sprintf("call.Query.Set(%q, %s)", q.str("name"), v))
		}
	}
	if x.paramsType != "" {
		sig = append(sig, "params *"+x.paramsType)
	}
	switch {
	case multipart:
		sig = append(sig, "filename string", "r io.Reader")
	case x.body != nil:
		sig = append(sig, "body *"+x.bodyType)
	}
	ret := x.retType
	isPtr := false
	switch {
	case ret == "":
		ret = "json.RawMessage"
	case ret == "string", strings.HasPrefix(ret, "[]"), strings.HasPrefix(ret, "map["):
	default:
		ret, isPtr = "*"+ret, true
	}
	doc := fmt.Sprintf("%s calls %s (%s %s): %s.", name, x.id, x.method, x.path, strings.TrimSuffix(x.summary, "."))
	if fileVariant {
		doc = fmt.Sprintf("%s calls %s (%s %s) with a multipart %q upload read from r.", name, x.id, x.method, x.path, x.fileField)
	}
	writeDoc(b, doc, "")
	if x.description != "" {
		b.WriteString("//\n")
		writeDoc(b, x.description, "")
	}
	fmt.Fprintf(b, "//\n// Required scope: %s.\n", x.scope)
	fmt.Fprintf(b, "func (s *%sService) %s(%s) (%s, error) {\n", pascal(x.tag), name, strings.Join(sig, ", "), ret)
	b.WriteString("call := Call{Query: url.Values{}}\n")
	if len(setPath) > 0 {
		fmt.Fprintf(b, "call.PathParams = map[string]string{%s}\n", strings.Join(setPath, ", "))
	}
	for _, s := range setQuery {
		b.WriteString(s + "\n")
	}
	if x.paramsType != "" {
		b.WriteString("for k, v := range params.values() {\ncall.Query[k] = v\n}\n")
	}
	switch {
	case multipart:
		b.WriteString("call.File = &File{Name: filename, Reader: r}\n")
	case x.body != nil:
		b.WriteString("if body != nil {\ncall.Body = body\n}\n")
	}
	if isPtr {
		fmt.Fprintf(b, "out := new(%s)\nif err := s.c.do(ctx, op%s, call, out); err != nil {\nreturn nil, err\n}\nreturn out, nil\n}\n\n", ret[1:], pascal(x.id))
	} else {
		fmt.Fprintf(b, "var out %s\nerr := s.c.do(ctx, op%s, call, &out)\nreturn out, err\n}\n\n", ret, pascal(x.id))
	}
}
