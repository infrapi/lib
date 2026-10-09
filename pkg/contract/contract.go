// Package contract writes an OpenAPI contract from a short description of a
// resource: its fields and the operations it offers. A service describes the
// resource in a few lines, and the contract — the file the server code is
// generated from, requests are validated against and /-/docs shows — follows.
//
//	name: bookmarks
//	version: 1
//	path: tenants/{tenant}
//	resource: bookmark
//	fields:
//	  url:   {type: string, required: true, max: 2000}
//	  title: {type: string, required: true, max: 200}
//	operations: [list, create, get, replace, delete]
//
// What every contract shares — the error format, the API key — is not written
// here: pkg/app adds it to every contract it serves.
package contract

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// The operations a resource can offer.
const (
	OpList    = "list"
	OpCreate  = "create"
	OpGet     = "get"
	OpReplace = "replace"
	OpDelete  = "delete"
)

var allOps = []string{OpList, OpCreate, OpGet, OpReplace, OpDelete}

// Field is one field of the resource.
type Field struct {
	Name string `yaml:"-"`
	// Type is string, integer, number, boolean, datetime, date or list.
	Type string `yaml:"type"`
	// Of is a list's item type: string, integer, number or boolean.
	Of          string `yaml:"of"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
	// Min and Max bound a string's length, a list's size, a number's value.
	Min *float64 `yaml:"min"`
	Max *float64 `yaml:"max"`
	// Pattern is a string's, or a list item's, regular expression.
	Pattern string   `yaml:"pattern"`
	Enum    []string `yaml:"enum"`
	Default any      `yaml:"default"`
	Example any      `yaml:"example"`
	// Computed fields are set by the service, never sent: they are in the
	// answers only, as a creation date is.
	Computed bool `yaml:"computed"`
}

// Resource is the description a contract is written from.
type Resource struct {
	// Name is the plural, lower-case name: the path's first segment and the
	// collection's, e.g. bookmarks.
	Name        string `yaml:"name"`
	Version     int    `yaml:"version"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Path is what comes between the version and the collection, with its
	// parameters: "tenants/{tenant}". Empty for none.
	Path string `yaml:"path"`
	// Params describes the path's parameters; a string when not described.
	Params map[string]Field `yaml:"params"`
	// Resource is the singular name, e.g. bookmark: the types are named
	// after it.
	Resource string `yaml:"resource"`
	// ID says who chooses an item's id: the service ("service", the default)
	// or the caller ("caller", an optional id field when creating).
	ID     string  `yaml:"id"`
	Fields []Field `yaml:"-"`
	// Filters are the list's query parameters, e.g. tag.
	Filters    []Field  `yaml:"-"`
	Operations []string `yaml:"operations"`
	// Example is a body to create one, shown in /-/docs, in its own order.
	Example yaml.Node `yaml:"example"`
	// Writes is the ring every write runs on; Rings gives one operation
	// another (x-infrapi-writes, x-infrapi-ring).
	Writes string            `yaml:"writes"`
	Rings  map[string]string `yaml:"rings"`
	// Broadcast says how a broadcast write answers; Broadcasts per operation
	// (x-infrapi-broadcast).
	Broadcast  string            `yaml:"broadcast"`
	Broadcasts map[string]string `yaml:"broadcasts"`
}

var (
	lowerName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	pathParam = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)
)

// Parse reads a resource description, fields and filters in the order they
// are written.
func Parse(data []byte) (*Resource, error) {
	var r Resource
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	var raw struct {
		Fields  yaml.Node `yaml:"fields"`
		Filters yaml.Node `yaml:"filters"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	var err error
	if r.Fields, err = orderedFields(&raw.Fields, "fields"); err != nil {
		return nil, err
	}
	if r.Filters, err = orderedFields(&raw.Filters, "filters"); err != nil {
		return nil, err
	}
	if err := checkKeys(data); err != nil {
		return nil, err
	}
	return &r, r.check()
}

// checkKeys refuses a key the format does not know: a typo would otherwise be
// dropped without a word.
func checkKeys(data []byte) error {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("contract: %w", err)
	}
	known := []string{"name", "version", "title", "description", "path", "params", "resource", "id", "fields",
		"filters", "operations", "example", "writes", "rings", "broadcast", "broadcasts"}
	for k := range top {
		if !slices.Contains(known, k) {
			return fmt.Errorf("contract: %q is not a key of the format (%s)", k, strings.Join(known, ", "))
		}
	}
	return nil
}

func orderedFields(n *yaml.Node, where string) ([]Field, error) {
	if n.Kind == 0 {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("contract: %s: a map of name to field", where)
	}
	fields := make([]Field, 0, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		var f Field
		value := n.Content[i+1]
		if err := value.Decode(&f); err != nil {
			return nil, fmt.Errorf("contract: %s.%s: %w", where, n.Content[i].Value, err)
		}
		var keys map[string]yaml.Node
		if err := value.Decode(&keys); err != nil {
			return nil, fmt.Errorf("contract: %s.%s: %w", where, n.Content[i].Value, err)
		}
		for k := range keys {
			if !slices.Contains([]string{"type", "of", "required", "description", "min", "max", "pattern", "enum",
				"default", "example", "computed"}, k) {
				return nil, fmt.Errorf("contract: %s.%s: %q is not a key of a field", where, n.Content[i].Value, k)
			}
		}
		f.Name = n.Content[i].Value
		fields = append(fields, f)
	}
	return fields, nil
}

// check refuses what would make a contract nobody meant.
func (r *Resource) check() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf("contract: "+format, args...)) }
	if !lowerName.MatchString(r.Name) {
		bad("name %q: lower-case letters, digits and _, e.g. bookmarks", r.Name)
	}
	if !lowerName.MatchString(r.Resource) {
		bad("resource %q: lower-case letters, digits and _, e.g. bookmark", r.Resource)
	}
	if r.Version < 1 {
		bad("version: 1 or more")
	}
	switch r.ID {
	case "", "service", "caller":
	default:
		bad("id %q: service or caller", r.ID)
	}
	if len(r.Operations) == 0 {
		bad("operations: at least one of %s", strings.Join(allOps, ", "))
	}
	for _, op := range r.Operations {
		if !slices.Contains(allOps, op) {
			bad("operations: %q is not one of %s", op, strings.Join(allOps, ", "))
		}
	}
	for op := range r.Rings {
		if !slices.Contains(r.Operations, op) {
			bad("rings: %q is not one of the operations", op)
		}
	}
	for op, b := range r.Broadcasts {
		if !slices.Contains(r.Operations, op) {
			bad("broadcasts: %q is not one of the operations", op)
		}
		if !slices.Contains([]string{"strict", "tolerant", "async"}, b) {
			bad("broadcasts.%s: %q is not strict, tolerant or async", op, b)
		}
	}
	if r.Broadcast != "" && !slices.Contains([]string{"strict", "tolerant", "async"}, r.Broadcast) {
		bad("broadcast: %q is not strict, tolerant or async", r.Broadcast)
	}
	if len(r.Filters) > 0 && !slices.Contains(r.Operations, OpList) {
		bad("filters: there is no list to filter")
	}
	names := map[string]bool{"id": r.ID == "caller"}
	for _, f := range r.Fields {
		if !lowerName.MatchString(f.Name) {
			bad("fields.%s: lower-case letters, digits and _", f.Name)
		}
		if f.Name == "id" {
			bad("fields.id: the id is the resource's own; set id: caller to let the caller choose it")
		}
		if names[f.Name] {
			bad("fields.%s: twice", f.Name)
		}
		names[f.Name] = true
		if err := f.check(); err != nil {
			bad("fields.%s: %v", f.Name, err)
		}
	}
	for _, f := range r.Filters {
		if err := f.check(); err != nil {
			bad("filters.%s: %v", f.Name, err)
		}
	}
	for _, m := range pathParam.FindAllStringSubmatch(r.Path, -1) {
		if m[1] == "id" {
			bad("path: {id} is the item's own parameter")
		}
	}
	for p := range r.Params {
		if !strings.Contains(r.Path, "{"+p+"}") {
			bad("params.%s: not in path %q", p, r.Path)
		}
	}
	return errors.Join(errs...)
}

func (f Field) check() error {
	switch f.Type {
	case "string", "integer", "number", "boolean", "datetime", "date":
		if f.Of != "" {
			return errors.New("of is for a list")
		}
	case "list":
		switch f.Of {
		case "string", "integer", "number", "boolean":
		default:
			return fmt.Errorf("a list needs of: string, integer, number or boolean, not %q", f.Of)
		}
	default:
		return fmt.Errorf("type %q: string, integer, number, boolean, datetime, date or list", f.Type)
	}
	if f.Computed && f.Required {
		return errors.New("computed and required: a computed field is not sent")
	}
	return nil
}

// Generate writes the resource's OpenAPI contract, as YAML.
func Generate(r *Resource) ([]byte, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	single, plural := pascal(r.Resource), pascal(r.Name)
	base := "/" + r.Name + "/v" + fmt.Sprint(r.Version)
	if p := strings.Trim(r.Path, "/"); p != "" {
		base += "/" + p
	}
	collection, item := base+"/"+r.Name, base+"/"+r.Name+"/{id}"

	title := r.Title
	if title == "" {
		title = plural
	}
	info := obj{{"title", title}, {"version", fmt.Sprint(r.Version)}}
	if r.Description != "" {
		info = append(info, kv{"description", r.Description})
	}
	doc := obj{{"openapi", "3.0.3"}, {"info", info}}
	if r.Writes != "" {
		doc = append(doc, kv{"x-infrapi-writes", r.Writes})
	}
	if r.Broadcast != "" {
		doc = append(doc, kv{"x-infrapi-broadcast", r.Broadcast})
	}

	var pathParams []any
	for _, m := range pathParam.FindAllStringSubmatch(r.Path, -1) {
		pathParams = append(pathParams, ref("#/components/parameters/"+pascal(m[1])))
	}
	idParam := obj{{"name", "id"}, {"in", "path"}, {"required", true},
		{"description", "The " + words(r.Resource) + "'s id."}, {"schema", obj{{"type", "string"}}}}

	op := func(name, id, summary string, extra ...kv) obj {
		o := obj{{"operationId", id}, {"summary", summary}}
		if ring, ok := r.Rings[name]; ok {
			o = append(o, kv{"x-infrapi-ring", ring})
		}
		if b, ok := r.Broadcasts[name]; ok {
			o = append(o, kv{"x-infrapi-broadcast", b})
		}
		return append(o, extra...)
	}
	jsonOf := func(schema any) obj { return obj{{"application/json", obj{{"schema", schema}}}} }
	answer := func(code, desc string, schema any) kv {
		return kv{"responses", obj{{code, obj{{"description", desc}, {"content", jsonOf(schema)}}}}}
	}
	body := func(withExample bool) kv {
		media := obj{{"schema", ref("#/components/schemas/" + single + "Spec")}}
		if withExample && r.Example.Kind != 0 {
			media = append(media, kv{"example", &r.Example})
		}
		return kv{"requestBody", obj{{"required", true}, {"content", obj{{"application/json", media}}}}}
	}
	has := func(o string) bool { return slices.Contains(r.Operations, o) }

	paths := obj{}
	if has(OpList) || has(OpCreate) {
		p := obj{}
		if len(pathParams) > 0 {
			p = append(p, kv{"parameters", pathParams})
		}
		if has(OpList) {
			extra := []kv{}
			if len(r.Filters) > 0 {
				var qs []any
				for _, f := range r.Filters {
					q := obj{{"name", f.Name}, {"in", "query"}}
					if f.Description != "" {
						q = append(q, kv{"description", f.Description})
					}
					qs = append(qs, append(q, kv{"schema", f.paramSchema()}))
				}
				extra = append(extra, kv{"parameters", qs})
			}
			extra = append(extra, answer("200", "The "+words(r.Name), obj{{"type", "array"},
				{"items", ref("#/components/schemas/" + single)}}))
			p = append(p, kv{"get", op(OpList, "list"+plural, "List the "+words(r.Name), extra...)})
		}
		if has(OpCreate) {
			p = append(p, kv{"post", op(OpCreate, "create"+single, "Create a "+words(r.Resource), body(true),
				answer("201", "Created", ref("#/components/schemas/"+single)))})
		}
		paths = append(paths, kv{collection, p})
	}
	if has(OpGet) || has(OpReplace) || has(OpDelete) {
		p := obj{{"parameters", append(slices.Clone(pathParams), idParam)}}
		if has(OpGet) {
			p = append(p, kv{"get", op(OpGet, "get"+single, "Read a "+words(r.Resource),
				answer("200", "The "+words(r.Resource), ref("#/components/schemas/"+single)))})
		}
		if has(OpReplace) {
			p = append(p, kv{"put", op(OpReplace, "replace"+single, "Replace a "+words(r.Resource), body(false),
				answer("200", "The "+words(r.Resource)+", after the change", ref("#/components/schemas/"+single)))})
		}
		if has(OpDelete) {
			p = append(p, kv{"delete", op(OpDelete, "delete"+single, "Delete a "+words(r.Resource),
				kv{"responses", obj{{"204", obj{{"description", "Deleted, or already gone"}}}}})})
		}
		paths = append(paths, kv{item, p})
	}
	doc = append(doc, kv{"paths", paths})

	components := obj{}
	if len(pathParams) > 0 {
		params := obj{}
		for _, m := range pathParam.FindAllStringSubmatch(r.Path, -1) {
			f, ok := r.Params[m[1]]
			if !ok {
				f = Field{Type: "string"}
			}
			pp := obj{{"name", m[1]}, {"in", "path"}, {"required", true}}
			if f.Description != "" {
				pp = append(pp, kv{"description", f.Description})
			}
			params = append(params, kv{pascal(m[1]), append(pp, kv{"schema", f.paramSchema()})})
		}
		components = append(components, kv{"parameters", params})
	}

	// What the caller sends, and what the service answers: the answer has the
	// id and the computed fields, and every field, always.
	spec, specRequired := obj{}, []any{}
	if r.ID == "caller" {
		spec = append(spec, kv{"id", obj{{"type", "string"},
			{"description", "The id to give it; the service chooses one when absent."}}})
	}
	full, fullRequired := obj{{"id", obj{{"type", "string"}}}}, []any{"id"}
	for _, f := range r.Fields {
		if !f.Computed {
			spec = append(spec, kv{f.Name, f.schema()})
			if f.Required {
				specRequired = append(specRequired, f.Name)
			}
		}
		full = append(full, kv{f.Name, f.answerSchema()})
		fullRequired = append(fullRequired, f.Name)
	}
	specSchema := obj{{"type", "object"}, {"additionalProperties", false}}
	if len(specRequired) > 0 {
		specSchema = append(specSchema, kv{"required", specRequired})
	}
	specSchema = append(specSchema, kv{"properties", spec})
	components = append(components, kv{"schemas", obj{
		{single + "Spec", specSchema},
		{single, obj{{"type", "object"}, {"required", fullRequired}, {"properties", full}}},
	}})
	doc = append(doc, kv{"components", components})

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	return out.Bytes(), nil
}

// schema is the field as the caller sends it.
func (f Field) schema() obj {
	s := obj{}
	switch f.Type {
	case "datetime":
		s = append(s, kv{"type", "string"}, kv{"format", "date-time"})
	case "date":
		s = append(s, kv{"type", "string"}, kv{"format", "date"})
	case "list":
		item := obj{{"type", f.Of}}
		if f.Pattern != "" {
			item = append(item, kv{"pattern", f.Pattern})
		}
		if len(f.Enum) > 0 {
			item = append(item, kv{"enum", f.Enum})
		}
		s = append(s, kv{"type", "array"}, kv{"items", item})
	default:
		s = append(s, kv{"type", f.Type})
	}
	if f.Description != "" {
		s = append(s, kv{"description", f.Description})
	}
	bound := func(minKey, maxKey string) {
		if f.Min != nil {
			s = append(s, kv{minKey, number(*f.Min)})
		}
		if f.Max != nil {
			s = append(s, kv{maxKey, number(*f.Max)})
		}
	}
	switch f.Type {
	case "string":
		bound("minLength", "maxLength")
		if f.Pattern != "" {
			s = append(s, kv{"pattern", f.Pattern})
		}
		if len(f.Enum) > 0 {
			s = append(s, kv{"enum", f.Enum})
		}
	case "list":
		bound("minItems", "maxItems")
	case "integer", "number":
		bound("minimum", "maximum")
	}
	if f.Default != nil {
		s = append(s, kv{"default", f.Default})
	}
	if f.Example != nil {
		s = append(s, kv{"example", f.Example})
	}
	return s
}

// paramSchema is a parameter's schema: its description is the parameter's.
func (f Field) paramSchema() obj {
	f.Description = ""
	return f.schema()
}

// answerSchema is the field as the service answers it: its type and meaning,
// not the caller's constraints.
func (f Field) answerSchema() obj {
	s := Field{Type: f.Type, Of: f.Of, Description: f.Description}.schema()
	return s
}

func number(f float64) any {
	if f == float64(int64(f)) {
		return int64(f)
	}
	return f
}

func ref(to string) obj { return obj{{"$ref", to}} }

// pascal writes bookmark_list as BookmarkList.
func pascal(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		r := []rune(part)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// words writes bookmark_list as "bookmark list".
func words(s string) string { return strings.ReplaceAll(s, "_", " ") }

// obj is a YAML mapping that keeps its keys in the order they are written:
// the contract reads top to bottom.
type obj []kv

type kv struct {
	k string
	v any
}

func (o obj) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, e := range o {
		var v yaml.Node
		if err := v.Encode(e.v); err != nil {
			return nil, err
		}
		// Keys are strings, "200" too: OpenAPI's response codes are.
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.k}, &v)
	}
	return n, nil
}
