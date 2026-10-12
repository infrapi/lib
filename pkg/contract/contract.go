// Package contract writes an OpenAPI contract from a short description of a
// service's resources: their fields and the operations they offer. A service
// describes them in a few lines, and the contract — the file the server code
// is generated from, requests are validated against and /-/docs shows —
// follows.
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
// A service of several resources lists them under resources, each with its
// own fields and operations, sharing the path, its parameters, the types and
// the errors.
//
// What every contract shares — the API key, a default error answer — pkg/app
// adds to every contract it serves.
package contract

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
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

// The ways the API's ids are given.
const (
	IDService = "service" // the service gives them, the default
	IDCaller  = "caller"  // the caller may give one, on creation
	IDInfrapi = "infrapi" // the library: UUIDs, paired with the backend's (x-infrapi-ids)
)

var builtinTypes = []string{"string", "integer", "number", "boolean", "datetime", "date", "list", "object", "map"}

// Field is one field of a resource, of a type, or a parameter.
type Field struct {
	Name string `yaml:"-"`
	// Type is string, integer, number, boolean, datetime, date, list, object
	// (any JSON object), map (string keys, values of Of), or one of the
	// description's types.
	Type string `yaml:"type"`
	// Of is a list's items, or a map's values: string, integer, number,
	// boolean, or — for a list — one of the description's types.
	Of string `yaml:"of"`
	// Required, Optional and Computed say who sets the field, as a Terraform
	// schema does: the caller must (required), may (optional, the default),
	// or the service does (computed) — or either (optional and computed: the
	// caller may, else the service does).
	Required    bool   `yaml:"required"`
	Optional    bool   `yaml:"optional"`
	Description string `yaml:"description"`
	// Min and Max bound a string's length, a list's size, a number's value.
	Min *float64 `yaml:"min"`
	Max *float64 `yaml:"max"`
	// Pattern is a string's, or a list item's, regular expression.
	Pattern string   `yaml:"pattern"`
	Enum    []string `yaml:"enum"`
	Default any      `yaml:"default"`
	Example any      `yaml:"example"`
	// Computed fields are set by the service: in the answers, and sent only
	// when Optional too.
	Computed bool `yaml:"computed"`
	// Sensitive fields are sent, and never answered: a password, a token.
	Sensitive bool `yaml:"sensitive"`
	// Immutable fields are sent on creation only: replacing the resource
	// keeps them (Terraform's ForceNew).
	Immutable bool `yaml:"immutable"`
	// Ref names the resource whose id the field holds — each item's, for a
	// list: with the library keeping that resource's ids, it is one of the
	// API's, turned into the backend's and back.
	Ref string `yaml:"ref"`
	// OmitEmpty leaves the field out of an answer when it has no value,
	// instead of always answering it: a parent a top-level folder has not.
	OmitEmpty bool `yaml:"omit_empty"`
}

var fieldKeys = []string{"type", "of", "required", "optional", "description", "min", "max", "pattern", "enum",
	"default", "example", "computed", "sensitive", "immutable", "ref", "omit_empty"}

// sent says whether the caller sends the field.
func (f Field) sent() bool { return !f.Computed || f.Optional }

// Cluster is a place in the service's rings (Spec.Roles).
type Cluster struct {
	// Writes is the ring the writes run on: every write of the contract
	// (x-infrapi-writes) at the top, every write of the resource in one.
	Writes string `yaml:"writes"`
	// Rings gives one operation a ring of its own (x-infrapi-ring); a read
	// can be given a leader's.
	Rings map[string]string `yaml:"rings"`
	// Broadcast says how a broadcast write answers — strict, tolerant or
	// async — and Broadcasts per operation (x-infrapi-broadcast).
	Broadcast  string            `yaml:"broadcast"`
	Broadcasts map[string]string `yaml:"broadcasts"`
}

// List is how a resource's list answers: an object with its items, and a
// page of them when Paging.
type List struct {
	// Paging adds the limit and page parameters, and page, limit and
	// next_page to the answer.
	Paging bool `yaml:"paging"`
	// Fields are those of an item in the list, when not all: a summary.
	Fields []string `yaml:"fields"`
}

// Doc documents one operation: its summary, its description, and the errors
// it gives besides those every operation does, by status.
type Doc struct {
	Summary     string         `yaml:"summary"`
	Description string         `yaml:"description"`
	Errors      map[int]string `yaml:"errors"`
}

// UnmarshalYAML takes a description alone as a string.
func (d *Doc) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		d.Description = n.Value
		return nil
	}
	type plain Doc
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	var keys map[string]yaml.Node
	if err := n.Decode(&keys); err != nil {
		return err
	}
	for k := range keys {
		if !slices.Contains([]string{"summary", "description", "errors"}, k) {
			return fmt.Errorf("%q is not one of summary, description, errors", k)
		}
	}
	*d = Doc(p)
	return nil
}

// Problem is what this service's errors carry besides the library's fields.
type Problem struct {
	// Codes are the codes its errors may have, documented on code.
	Codes []string `yaml:"codes"`
	// Fields are its own fields.
	Fields []Field `yaml:"-"`
}

// Resource is one resource of the service.
type Resource struct {
	// Name is the plural, lower-case name of the collection, e.g. bookmarks.
	Name string `yaml:"name"`
	// Resource is the singular name, e.g. bookmark: the types are named
	// after it.
	Resource    string `yaml:"resource"`
	Description string `yaml:"description"`
	// ID says whose id the API answers, the service's by default.
	ID     string  `yaml:"id"`
	Fields []Field `yaml:"-"`
	// Filters are the list's query parameters, e.g. tag.
	Filters    []Field  `yaml:"-"`
	Operations []string `yaml:"operations"`
	// List shapes the list's answer; nil for a plain array of the items.
	List *List `yaml:"list"`
	// Query are an operation's own query parameters: a delete's force.
	Query map[string][]Field `yaml:"-"`
	// Docs documents the operations.
	Docs map[string]Doc `yaml:"docs"`
	// Example is a body to create one, shown in /-/docs, in its own order.
	Example yaml.Node `yaml:"example"`
	// Cluster says which ring each operation runs on, and how a broadcast
	// write answers.
	Cluster Cluster `yaml:"cluster"`
}

// Type is an object a field holds, or the items of a list hold.
type Type struct {
	Name   string
	Fields []Field
}

// Service is the description a contract is written from.
type Service struct {
	// Name is the plural, lower-case name of the service: the path's first
	// segment, e.g. bookmarks; a single resource's collection too.
	Name        string `yaml:"name"`
	Version     int    `yaml:"version"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Path is what comes between the version and the collections, with its
	// parameters: "tenants/{tenant}". Empty for none.
	Path string `yaml:"path"`
	// Params describes the path's parameters; a string when not described.
	Params map[string]Field `yaml:"params"`
	// ID is every resource's way of giving ids, unless it says its own.
	ID string `yaml:"id"`
	// Errors are the errors every operation may give, by status. 400 and 404
	// go only where they can happen: a body or a parameter, an item.
	Errors map[int]string `yaml:"errors"`
	// Problem is what the errors carry besides the library's fields.
	Problem   Problem     `yaml:"problem"`
	Types     []Type      `yaml:"-"`
	Cluster   Cluster     `yaml:"cluster"`
	Resources []*Resource `yaml:"-"`
}

var (
	lowerName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	pathParam = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)
)

var (
	serviceKeys  = []string{"name", "version", "title", "description", "path", "params", "id", "errors", "problem", "types", "cluster", "resources"}
	resourceKeys = []string{"name", "resource", "description", "id", "fields", "filters", "operations", "list", "query", "docs", "example", "cluster"}
	// What a single resource's description holds at the top, besides the
	// service's keys.
	singleKeys = []string{"resource", "fields", "filters", "operations", "list", "query", "docs", "example"}
)

// Parse reads a description: a service of several resources, or of one
// described at the top. Fields keep the order they are written in.
func Parse(data []byte) (*Service, error) {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	_, multi := top["resources"]
	known := serviceKeys
	if !multi {
		known = append(slices.Clone(serviceKeys), singleKeys...)
	}
	for k := range top {
		if !slices.Contains(known, k) {
			if multi && slices.Contains(singleKeys, k) {
				return nil, fmt.Errorf("contract: %q belongs to a resource, under resources", k)
			}
			return nil, fmt.Errorf("contract: %q is not a key of the format (%s)", k, strings.Join(known, ", "))
		}
	}

	var s Service
	if err := decodeLoose(data, &s); err != nil {
		return nil, err
	}
	var raw struct {
		Types     yaml.Node `yaml:"types"`
		Problem   yaml.Node `yaml:"problem"`
		Cluster   yaml.Node `yaml:"cluster"`
		Resources yaml.Node `yaml:"resources"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	if err := checkCluster(&raw.Cluster, "cluster", multi); err != nil {
		return nil, err
	}
	if err := parseProblem(&raw.Problem, &s.Problem); err != nil {
		return nil, err
	}
	if raw.Types.Kind != 0 {
		if raw.Types.Kind != yaml.MappingNode {
			return nil, errors.New("contract: types: a map of name to fields")
		}
		for i := 0; i < len(raw.Types.Content); i += 2 {
			name := raw.Types.Content[i].Value
			fields, err := orderedFields(raw.Types.Content[i+1], "types."+name)
			if err != nil {
				return nil, err
			}
			s.Types = append(s.Types, Type{Name: name, Fields: fields})
		}
	}

	if multi {
		if raw.Resources.Kind != yaml.SequenceNode {
			return nil, errors.New("contract: resources: a list of resources")
		}
		for i, n := range raw.Resources.Content {
			r, err := parseResource(n, fmt.Sprintf("resources[%d]", i))
			if err != nil {
				return nil, err
			}
			s.Resources = append(s.Resources, r)
		}
	} else {
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("contract: %w", err)
		}
		r, err := parseResource(doc.Content[0], "")
		if err != nil {
			return nil, err
		}
		// A single resource is the service's collection, and the service's
		// cluster's rings are its operations'.
		r.Name, r.Description, r.ID = s.Name, "", ""
		r.Cluster = Cluster{Rings: s.Cluster.Rings, Broadcasts: s.Cluster.Broadcasts}
		s.Cluster.Rings, s.Cluster.Broadcasts = nil, nil
		s.Resources = []*Resource{r}
	}
	return &s, s.check()
}

// decodeLoose decodes what the struct's tags name, the rest left to the
// callers.
func decodeLoose(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("contract: %w", err)
	}
	return nil
}

func parseResource(n *yaml.Node, where string) (*Resource, error) {
	prefix := ""
	if where != "" {
		prefix = where + "."
		var keys map[string]yaml.Node
		if err := n.Decode(&keys); err != nil {
			return nil, fmt.Errorf("contract: %s: %w", where, err)
		}
		for k := range keys {
			if !slices.Contains(resourceKeys, k) {
				return nil, fmt.Errorf("contract: %s: %q is not a key of a resource (%s)", where, k, strings.Join(resourceKeys, ", "))
			}
		}
	}
	var r Resource
	if err := n.Decode(&r); err != nil {
		return nil, fmt.Errorf("contract: %s%w", prefix, err)
	}
	var raw struct {
		Fields  yaml.Node `yaml:"fields"`
		Filters yaml.Node `yaml:"filters"`
		Query   yaml.Node `yaml:"query"`
		List    yaml.Node `yaml:"list"`
		Cluster yaml.Node `yaml:"cluster"`
	}
	if err := n.Decode(&raw); err != nil {
		return nil, fmt.Errorf("contract: %s%w", prefix, err)
	}
	var err error
	if r.Fields, err = orderedFields(&raw.Fields, prefix+"fields"); err != nil {
		return nil, err
	}
	if r.Filters, err = orderedFields(&raw.Filters, prefix+"filters"); err != nil {
		return nil, err
	}
	if raw.Query.Kind != 0 {
		if raw.Query.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("contract: %squery: a map of operation to parameters", prefix)
		}
		r.Query = map[string][]Field{}
		for i := 0; i < len(raw.Query.Content); i += 2 {
			op := raw.Query.Content[i].Value
			if r.Query[op], err = orderedFields(raw.Query.Content[i+1], prefix+"query."+op); err != nil {
				return nil, err
			}
		}
	}
	if raw.List.Kind == yaml.MappingNode {
		var keys map[string]yaml.Node
		if err := raw.List.Decode(&keys); err != nil {
			return nil, fmt.Errorf("contract: %slist: %w", prefix, err)
		}
		for k := range keys {
			if k != "paging" && k != "fields" {
				return nil, fmt.Errorf("contract: %slist: %q is not one of paging, fields", prefix, k)
			}
		}
	}
	if where != "" {
		if err := checkCluster(&raw.Cluster, prefix+"cluster", false); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// checkCluster refuses a key a cluster does not have. At the top of a
// service of several resources, rings and broadcasts are each resource's.
func checkCluster(n *yaml.Node, where string, top bool) error {
	if n.Kind == 0 {
		return nil
	}
	var keys map[string]yaml.Node
	if err := n.Decode(&keys); err != nil {
		return fmt.Errorf("contract: %s: %w", where, err)
	}
	allowed := []string{"writes", "rings", "broadcast", "broadcasts"}
	if top {
		allowed = []string{"writes", "broadcast"}
	}
	for k := range keys {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("contract: %s: %q is not one of %s", where, k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func parseProblem(n *yaml.Node, p *Problem) error {
	if n.Kind == 0 {
		return nil
	}
	var raw struct {
		Codes  []string  `yaml:"codes"`
		Fields yaml.Node `yaml:"fields"`
	}
	var keys map[string]yaml.Node
	if err := n.Decode(&keys); err != nil {
		return fmt.Errorf("contract: problem: %w", err)
	}
	for k := range keys {
		if k != "codes" && k != "fields" {
			return fmt.Errorf("contract: problem: %q is not one of codes, fields", k)
		}
	}
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("contract: problem: %w", err)
	}
	fields, err := orderedFields(&raw.Fields, "problem.fields")
	if err != nil {
		return err
	}
	p.Codes, p.Fields = raw.Codes, fields
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
			if !slices.Contains(fieldKeys, k) {
				return nil, fmt.Errorf("contract: %s.%s: %q is not a key of a field", where, n.Content[i].Value, k)
			}
		}
		f.Name = n.Content[i].Value
		fields = append(fields, f)
	}
	return fields, nil
}

// idOf is a resource's way of giving ids.
func (s *Service) idOf(r *Resource) string {
	switch {
	case r.ID != "":
		return r.ID
	case s.ID != "":
		return s.ID
	}
	return IDService
}

// byResource is the resource of that singular name.
func (s *Service) byResource(name string) *Resource {
	for _, r := range s.Resources {
		if r.Resource == name {
			return r
		}
	}
	return nil
}

func (s *Service) typeNamed(name string) bool {
	for _, t := range s.Types {
		if t.Name == name {
			return true
		}
	}
	return false
}

// check refuses what would make a contract nobody meant.
func (s *Service) check() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf("contract: "+format, args...)) }
	if !lowerName.MatchString(s.Name) {
		bad("name %q: lower-case letters, digits and _, e.g. bookmarks", s.Name)
	}
	if s.Version < 1 {
		bad("version: 1 or more")
	}
	idOK := func(id string) bool { return slices.Contains([]string{"", IDService, IDCaller, IDInfrapi}, id) }
	if !idOK(s.ID) {
		bad("id %q: service, caller or infrapi", s.ID)
	}
	broadcastOK := func(b string) bool { return slices.Contains([]string{"strict", "tolerant", "async"}, b) }
	if s.Cluster.Broadcast != "" && !broadcastOK(s.Cluster.Broadcast) {
		bad("cluster.broadcast: %q is not strict, tolerant or async", s.Cluster.Broadcast)
	}
	for code := range s.Errors {
		if code < 400 || code > 599 {
			bad("errors.%d: an error is 4xx or 5xx", code)
		}
	}
	for _, m := range pathParam.FindAllStringSubmatch(s.Path, -1) {
		if m[1] == "id" {
			bad("path: {id} is the item's own parameter")
		}
	}
	for p, f := range s.Params {
		if !strings.Contains(s.Path, "{"+p+"}") {
			bad("params.%s: not in path %q", p, s.Path)
		}
		if err := s.checkField(f, false); err != nil {
			bad("params.%s: %v", p, err)
		}
	}
	for _, f := range s.Problem.Fields {
		if err := s.checkField(f, true); err != nil {
			bad("problem.fields.%s: %v", f.Name, err)
		}
	}
	types := map[string]bool{}
	for _, t := range s.Types {
		if !lowerName.MatchString(t.Name) || slices.Contains(builtinTypes, t.Name) {
			bad("types.%s: lower-case letters, digits and _, and not a built-in type", t.Name)
		}
		if types[t.Name] {
			bad("types.%s: twice", t.Name)
		}
		types[t.Name] = true
		for _, f := range t.Fields {
			if err := s.checkField(f, true); err != nil {
				bad("types.%s.%s: %v", t.Name, f.Name, err)
			}
		}
	}

	if len(s.Resources) == 0 {
		bad("resources: at least one")
	}
	collections := map[string]bool{}
	for _, r := range s.Resources {
		at := r.Name + "."
		if len(s.Resources) == 1 {
			at = ""
		}
		if !lowerName.MatchString(r.Name) {
			bad("%sname %q: lower-case letters, digits and _, e.g. folders", at, r.Name)
		}
		if collections[r.Name] {
			bad("resources: %q twice", r.Name)
		}
		collections[r.Name] = true
		if !lowerName.MatchString(r.Resource) {
			bad("%sresource %q: lower-case letters, digits and _, e.g. bookmark", at, r.Resource)
		}
		if !idOK(r.ID) {
			bad("%sid %q: service, caller or infrapi", at, r.ID)
		}
		if len(r.Operations) == 0 {
			bad("%soperations: at least one of %s", at, strings.Join(allOps, ", "))
		}
		for _, op := range r.Operations {
			if !slices.Contains(allOps, op) {
				bad("%soperations: %q is not one of %s", at, op, strings.Join(allOps, ", "))
			}
		}
		for op := range r.Cluster.Rings {
			if !slices.Contains(r.Operations, op) {
				bad("%scluster.rings: %q is not one of the operations", at, op)
			}
		}
		for op, b := range r.Cluster.Broadcasts {
			if !slices.Contains(r.Operations, op) {
				bad("%scluster.broadcasts: %q is not one of the operations", at, op)
			}
			if !broadcastOK(b) {
				bad("%scluster.broadcasts.%s: %q is not strict, tolerant or async", at, op, b)
			}
		}
		if r.Cluster.Broadcast != "" && !broadcastOK(r.Cluster.Broadcast) {
			bad("%scluster.broadcast: %q is not strict, tolerant or async", at, r.Cluster.Broadcast)
		}
		has := slices.Contains[[]string]
		if len(r.Filters) > 0 && !has(r.Operations, OpList) {
			bad("%sfilters: there is no list to filter", at)
		}
		if r.List != nil && !has(r.Operations, OpList) {
			bad("%slist: there is no list", at)
		}
		for op := range r.Query {
			if !has(r.Operations, op) {
				bad("%squery: %q is not one of the operations", at, op)
			}
		}
		for op, d := range r.Docs {
			if !has(r.Operations, op) {
				bad("%sdocs: %q is not one of the operations", at, op)
			}
			for code := range d.Errors {
				if code < 400 || code > 599 {
					bad("%sdocs.%s.errors.%d: an error is 4xx or 5xx", at, op, code)
				}
			}
		}
		names := map[string]bool{"id": s.idOf(r) == IDCaller}
		for _, f := range r.Fields {
			if !lowerName.MatchString(f.Name) {
				bad("%sfields.%s: lower-case letters, digits and _", at, f.Name)
			}
			if f.Name == "id" {
				bad("%sfields.id: the id is the resource's own; set id: caller to let the caller choose it", at)
			}
			if names[f.Name] {
				bad("%sfields.%s: twice", at, f.Name)
			}
			names[f.Name] = true
			if err := s.checkField(f, true); err != nil {
				bad("%sfields.%s: %v", at, f.Name, err)
			}
		}
		if r.List != nil {
			for _, name := range r.List.Fields {
				if !names[name] || name == "id" {
					bad("%slist.fields: %q is not one of the fields", at, name)
				}
			}
		}
		for _, f := range r.Filters {
			if err := s.checkField(f, false); err != nil {
				bad("%sfilters.%s: %v", at, f.Name, err)
			}
		}
		for op, fs := range r.Query {
			for _, f := range fs {
				if err := s.checkField(f, false); err != nil {
					bad("%squery.%s.%s: %v", at, op, f.Name, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// checkField refuses a field nobody meant; objects only where a body is.
func (s *Service) checkField(f Field, inBody bool) error {
	scalar := func(t string) bool {
		return slices.Contains([]string{"string", "integer", "number", "boolean"}, t)
	}
	switch {
	case f.Type == "list":
		if !scalar(f.Of) && (!inBody || !s.typeNamed(f.Of)) {
			return fmt.Errorf("a list needs of: string, integer, number or boolean, or one of the types, not %q", f.Of)
		}
	case f.Type == "map":
		if !scalar(f.Of) {
			return fmt.Errorf("a map needs of: string, integer, number or boolean, not %q", f.Of)
		}
	case slices.Contains(builtinTypes, f.Type) || (inBody && s.typeNamed(f.Type)):
		if f.Of != "" {
			return errors.New("of is for a list or a map")
		}
	default:
		return fmt.Errorf("type %q: string, integer, number, boolean, datetime, date, list, object, map, or one of the types", f.Type)
	}
	if (f.Type == "object" || f.Type == "map" || s.typeNamed(f.Type)) && !inBody {
		return errors.New("an object is for a body")
	}
	if f.Ref != "" {
		target := s.byResource(f.Ref)
		switch {
		case target == nil:
			return fmt.Errorf("ref %q: not one of the resources", f.Ref)
		case f.Type != "string" && (f.Type != "list" || f.Of != "string"):
			return errors.New("ref: an id is a string, or a list of strings")
		}
	}
	switch {
	case f.Required && f.Optional:
		return errors.New("required and optional: one or the other")
	case f.Required && f.Computed:
		return errors.New("required and computed: the caller sends it, or the service sets it; optional and computed for either")
	case f.Sensitive && f.Computed:
		return errors.New("sensitive and computed: a sensitive field is never answered, a computed one is")
	case f.Required && f.Default != nil:
		return errors.New("required with a default: a default is for a field the caller may leave out")
	case f.Immutable && !f.sent():
		return errors.New("immutable and computed: an immutable field is sent on creation; optional and computed for either")
	case f.OmitEmpty && f.Sensitive:
		return errors.New("omit_empty and sensitive: a sensitive field is never answered")
	}
	return nil
}

// Generate writes the service's OpenAPI contract, as YAML.
func Generate(s *Service) ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	g := &gen{s: s}
	return g.contract()
}

type gen struct{ s *Service }

func (g *gen) contract() ([]byte, error) {
	s := g.s
	title := s.Title
	if title == "" {
		title = pascal(s.Name)
	}
	info := obj{{"title", title}, {"version", fmt.Sprint(s.Version)}}
	if s.Description != "" {
		info = append(info, kv{"description", s.Description})
	}
	doc := obj{{"openapi", "3.0.3"}, {"info", info}}
	for _, r := range s.Resources {
		if s.idOf(r) == IDInfrapi {
			doc = append(doc, kv{"x-infrapi-ids", s.Name})
			break
		}
	}
	if s.Cluster.Writes != "" {
		doc = append(doc, kv{"x-infrapi-writes", s.Cluster.Writes})
	}
	if s.Cluster.Broadcast != "" {
		doc = append(doc, kv{"x-infrapi-broadcast", s.Cluster.Broadcast})
	}
	if len(s.Resources) > 1 {
		var tags []any
		for _, r := range s.Resources {
			t := obj{{"name", r.Name}}
			if r.Description != "" {
				t = append(t, kv{"description", r.Description})
			}
			tags = append(tags, t)
		}
		doc = append(doc, kv{"tags", tags})
	}

	paths := obj{}
	for _, r := range s.Resources {
		paths = append(paths, g.paths(r)...)
	}
	doc = append(doc, kv{"paths", paths})

	components := obj{}
	params := obj{}
	if ms := pathParam.FindAllStringSubmatch(s.Path, -1); len(ms) > 0 {
		for _, m := range ms {
			f, ok := s.Params[m[1]]
			if !ok {
				f = Field{Type: "string"}
			}
			pp := obj{{"name", m[1]}, {"in", "path"}, {"required", true}}
			if f.Description != "" {
				pp = append(pp, kv{"description", f.Description})
			}
			params = append(params, kv{pascal(m[1]), append(pp, kv{"schema", g.schema(f, modeParam)})})
		}
	}
	params = append(params,
		kv{"CorrelationID", obj{{"name", "X-Correlation-ID"}, {"in", "header"},
			{"description", "Carried into every log line this call produces, and returned. Generated when absent."},
			{"schema", obj{{"type", "string"}, {"maxLength", 128}}}}},
		kv{"IdempotencyKey", obj{{"name", "Idempotency-Key"}, {"in", "header"},
			{"description", "Makes a write safe to repeat: the same key with the same body replays the first " +
				"answer and does nothing again; with another body it is refused. Kept in the service's database."},
			{"schema", obj{{"type", "string"}, {"minLength", 1}, {"maxLength", 255}}}}})
	components = append(components, kv{"parameters", params})
	responses := obj{}
	shared := g.shared()
	codes := make([]int, 0, len(shared))
	for code := range shared {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	for _, code := range codes {
		responses = append(responses, kv{"Problem" + strconv.Itoa(code), problemAnswer(shared[code])})
	}
	components = append(components, kv{"responses", responses})
	schemas := obj{}
	for _, t := range s.Types {
		schemas = append(schemas, kv{pascal(t.Name), g.object(t.Fields, nil)})
	}
	for _, r := range s.Resources {
		schemas = append(schemas, g.schemas(r)...)
	}
	schemas = append(schemas, kv{"Problem", g.problem()})
	components = append(components, kv{"schemas", schemas})
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

// paths are a resource's collection and item paths.
func (g *gen) paths(r *Resource) obj {
	s := g.s
	single, plural := pascal(r.Resource), pascal(r.Name)
	base := "/" + s.Name + "/v" + fmt.Sprint(s.Version)
	if p := strings.Trim(s.Path, "/"); p != "" {
		base += "/" + p
	}
	collection, item := base+"/"+r.Name, base+"/"+r.Name+"/{id}"

	var pathParams []any
	for _, m := range pathParam.FindAllStringSubmatch(s.Path, -1) {
		pathParams = append(pathParams, ref("#/components/parameters/"+pascal(m[1])))
	}
	idParam := obj{{"name", "id"}, {"in", "path"}, {"required", true},
		{"description", "The " + words(r.Resource) + "'s id."}, {"schema", g.idSchema(r)}}

	jsonOf := func(schema any) obj { return obj{{"application/json", obj{{"schema", schema}}}} }
	updateSchema := single + "Spec"
	for _, f := range r.Fields {
		if f.Immutable {
			updateSchema = single + "Update" // replacing keeps what was set on creation
		}
	}
	body := func(create bool) kv {
		schema := single + "Spec"
		if !create {
			schema = updateSchema
		}
		media := obj{{"schema", ref("#/components/schemas/" + schema)}}
		if create && r.Example.Kind != 0 {
			media = append(media, kv{"example", &r.Example})
		}
		return kv{"requestBody", obj{{"required", true}, {"content", obj{{"application/json", media}}}}}
	}
	has := func(o string) bool { return slices.Contains(r.Operations, o) }

	// op is one operation, with its documentation, its parameters, its body
	// and its answers: the success, then the errors it may give.
	op := func(name, id, summary string, query []any, withBody bool, success obj) obj {
		d := r.Docs[name]
		if d.Summary != "" {
			summary = d.Summary
		}
		o := obj{{"operationId", id}, {"summary", summary}}
		if d.Description != "" {
			o = append(o, kv{"description", d.Description})
		}
		if len(s.Resources) > 1 {
			o = append(o, kv{"tags", []any{r.Name}})
		}
		ring, ok := r.Cluster.Rings[name]
		if !ok && name != OpList && name != OpGet && r.Cluster.Writes != "" {
			ring, ok = r.Cluster.Writes, true
		}
		if ok {
			o = append(o, kv{"x-infrapi-ring", ring})
		}
		if b, ok := r.Cluster.Broadcasts[name]; ok {
			o = append(o, kv{"x-infrapi-broadcast", b})
		} else if r.Cluster.Broadcast != "" && name != OpList && name != OpGet {
			o = append(o, kv{"x-infrapi-broadcast", r.Cluster.Broadcast})
		}
		for _, f := range r.Query[name] {
			query = append(query, g.queryParam(f))
		}
		// What pkg/app does with every call: its correlation id, and a
		// write's Idempotency-Key.
		query = append(query, ref("#/components/parameters/CorrelationID"))
		if name != OpList && name != OpGet {
			query = append(query, ref("#/components/parameters/IdempotencyKey"))
		}
		if len(query) > 0 {
			o = append(o, kv{"parameters", query})
		}
		if withBody {
			o = append(o, body(name == OpCreate))
		}
		item := name == OpGet || name == OpReplace
		return append(o, kv{"responses", append(success, g.errors(r, name, withBody || len(query) > 0, item)...)})
	}
	answer := func(code, desc string, schema any) obj {
		a := obj{{"description", desc}}
		if schema != nil {
			a = append(a, kv{"content", jsonOf(schema)})
		}
		return obj{{code, a}}
	}

	out := obj{}
	if has(OpList) || has(OpCreate) {
		p := obj{}
		if len(pathParams) > 0 {
			p = append(p, kv{"parameters", pathParams})
		}
		if has(OpList) {
			var query []any
			for _, f := range r.Filters {
				query = append(query, g.queryParam(f))
			}
			var listSchema any = obj{{"type", "array"}, {"items", ref("#/components/schemas/" + single)}}
			if r.List != nil {
				listSchema = ref("#/components/schemas/" + single + "List")
				if r.List.Paging {
					query = append(query,
						obj{{"name", "limit"}, {"in", "query"}, {"description", "How many items a page holds."},
							{"schema", obj{{"type", "integer"}, {"minimum", 1}, {"maximum", 1000}, {"default", 100}}}},
						obj{{"name", "page"}, {"in", "query"}, {"description", "The page, from 1."},
							{"schema", obj{{"type", "integer"}, {"minimum", 1}, {"default", 1}}}})
				}
			}
			p = append(p, kv{"get", op(OpList, "list"+plural, "List the "+words(r.Name), query, false,
				answer("200", "The "+words(r.Name), listSchema))})
		}
		if has(OpCreate) {
			p = append(p, kv{"post", op(OpCreate, "create"+single, "Create a "+words(r.Resource), nil, true,
				answer("201", "Created", ref("#/components/schemas/"+single)))})
		}
		out = append(out, kv{collection, p})
	}
	if has(OpGet) || has(OpReplace) || has(OpDelete) {
		p := obj{{"parameters", append(slices.Clone(pathParams), idParam)}}
		if has(OpGet) {
			p = append(p, kv{"get", op(OpGet, "get"+single, "Read a "+words(r.Resource), nil, false,
				answer("200", "The "+words(r.Resource), ref("#/components/schemas/"+single)))})
		}
		if has(OpReplace) {
			p = append(p, kv{"put", op(OpReplace, "replace"+single, "Replace a "+words(r.Resource), nil, true,
				answer("200", "The "+words(r.Resource)+", after the change", ref("#/components/schemas/"+single)))})
		}
		if has(OpDelete) {
			p = append(p, kv{"delete", op(OpDelete, "delete"+single, "Delete a "+words(r.Resource), nil, false,
				answer("204", "Deleted, or already gone", nil))})
		}
		out = append(out, kv{item, p})
	}
	return out
}

// errors are an operation's error answers, by status: the service's, 400
// where a body or a parameter can be wrong, 404 on an item, and its own.
func (g *gen) errors(r *Resource, op string, input, item bool) obj {
	descs := map[int]string{}
	if input {
		descs[400] = "The request is malformed. Not retryable."
	}
	if item {
		descs[404] = "No such " + words(r.Resource) + ". Not retryable."
	}
	for code, d := range g.s.Errors {
		_, applies := descs[code]
		if (code == 400 || code == 404) && !applies {
			continue
		}
		descs[code] = d
	}
	for code, d := range r.Docs[op].Errors {
		descs[code] = d
	}
	codes := make([]int, 0, len(descs))
	for code := range descs {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	out := obj{}
	for _, code := range codes {
		// The service's own, the same everywhere, are written once.
		if shared, ok := g.shared()[code]; ok && shared == descs[code] {
			out = append(out, kv{strconv.Itoa(code), ref("#/components/responses/Problem" + strconv.Itoa(code))})
			continue
		}
		out = append(out, kv{strconv.Itoa(code), problemAnswer(descs[code])})
	}
	return out
}

// shared are the error answers every operation shares, by status: the
// service's, and the generic 400.
func (g *gen) shared() map[int]string {
	out := map[int]string{400: "The request is malformed. Not retryable."}
	for code, d := range g.s.Errors {
		out[code] = d
	}
	return out
}

func problemAnswer(desc string) obj {
	return obj{{"description", desc},
		{"content", obj{{"application/problem+json", obj{{"schema", ref("#/components/schemas/Problem")}}}}}}
}

func (g *gen) queryParam(f Field) obj {
	q := obj{{"name", f.Name}, {"in", "query"}}
	if f.Description != "" {
		q = append(q, kv{"description", f.Description})
	}
	return append(q, kv{"schema", g.schema(f, modeParam)})
}

// idSchema is a resource's id: marked as one when the library keeps it.
func (g *gen) idSchema(r *Resource) obj {
	s := obj{{"type", "string"}}
	if g.s.idOf(r) == IDInfrapi {
		s = append(s, kv{"x-infrapi-id", r.Name})
	}
	return s
}

// schemas are what a resource's caller sends and is answered.
func (g *gen) schemas(r *Resource) obj {
	single := pascal(r.Resource)
	// What the caller sends — on creation, and on replacing when immutable
	// fields make it differ.
	input := func(create bool) obj {
		var fields []Field
		for _, f := range r.Fields {
			if f.sent() && (create || !f.Immutable) {
				fields = append(fields, f)
			}
		}
		var id obj
		if g.s.idOf(r) == IDCaller && create {
			id = obj{{"type", "string"}, {"description", "The id to give it; the service chooses one when absent."}}
		}
		return g.object(fields, id)
	}
	out := obj{{single + "Spec", input(true)}}
	for _, f := range r.Fields {
		if f.Immutable {
			out = append(out, kv{single + "Update", input(false)})
			break
		}
	}
	answer := func(only []string) obj {
		props, required := obj{{"id", g.idSchema(r)}}, []any{"id"}
		for _, f := range r.Fields {
			if f.Sensitive || (only != nil && !slices.Contains(only, f.Name)) {
				continue
			}
			props = append(props, kv{f.Name, g.schema(f, modeAnswer)})
			if !f.OmitEmpty {
				required = append(required, f.Name)
			}
		}
		return obj{{"type", "object"}, {"required", required}, {"properties", props}}
	}
	out = append(out, kv{single, answer(nil)})
	if r.List != nil {
		itemRef := "#/components/schemas/" + single
		if len(r.List.Fields) > 0 {
			sum := answer(r.List.Fields)
			out = append(out, kv{single + "Summary", append(obj{{"description",
				"A " + words(r.Resource) + " in a list: get it for the rest."}}, sum...)})
			itemRef += "Summary"
		}
		props := obj{{"items", obj{{"type", "array"}, {"items", ref(itemRef)}}}}
		required := []any{"items"}
		if r.List.Paging {
			props = append(props, kv{"page", obj{{"type", "integer"}}}, kv{"limit", obj{{"type", "integer"}}},
				kv{"next_page", obj{{"type", "integer"},
					{"description", "Present when this page is full. The page it points to may be empty."}}})
			required = append(required, "page", "limit")
		}
		out = append(out, kv{single + "List", obj{{"type", "object"}, {"required", required}, {"properties", props}}})
	}
	return out
}

// object is a body's object of those fields, with an id first when given.
func (g *gen) object(fields []Field, id obj) obj {
	props, required := obj{}, []any{}
	if id != nil {
		props = append(props, kv{"id", id})
	}
	for _, f := range fields {
		props = append(props, kv{f.Name, g.schema(f, modeInput)})
		if f.Required {
			required = append(required, f.Name)
		}
	}
	s := obj{{"type", "object"}, {"additionalProperties", false}}
	if len(required) > 0 {
		s = append(s, kv{"required", required})
	}
	return append(s, kv{"properties", props})
}

// problem is the error format: the library's fields, and the service's own.
func (g *gen) problem() obj {
	code := obj{{"type", "string"}}
	desc := "Stable and machine readable."
	if len(g.s.Problem.Codes) > 0 {
		desc += " One of: " + strings.Join(g.s.Problem.Codes, ", ") + "."
	}
	code = append(code, kv{"description", desc})
	props := obj{
		{"status", obj{{"type", "integer"}}},
		{"code", code},
		{"title", obj{{"type", "string"}}},
		{"detail", obj{{"type", "string"}}},
		{"retryable", obj{{"type", "boolean"}, {"description", "Whether the same call may succeed later."}}},
		{"correlation_id", obj{{"type", "string"}, {"description", "The call's id, in the service's logs too."}}},
	}
	for _, f := range g.s.Problem.Fields {
		props = append(props, kv{f.Name, g.schema(f, modeAnswer)})
	}
	return obj{{"type", "object"}, {"description", "An error, as an RFC 9457 problem document."},
		{"required", []any{"status", "code", "title"}}, {"properties", props}}
}

// How a field is written: as the caller sends it, with its rules; as the
// service answers it, its type and meaning; as a parameter, without its
// description, which is the parameter's.
type mode int

const (
	modeInput mode = iota
	modeAnswer
	modeParam
)

func (g *gen) schema(f Field, m mode) obj {
	if m == modeAnswer {
		f = Field{Name: f.Name, Type: f.Type, Of: f.Of, Description: f.Description, Ref: f.Ref}
	}
	if m == modeParam {
		f.Description = ""
	}
	s := obj{}
	scalar := func(t string) obj {
		if g.s.typeNamed(t) {
			return ref("#/components/schemas/" + pascal(t))
		}
		return obj{{"type", t}}
	}
	switch f.Type {
	case "datetime":
		s = append(s, kv{"type", "string"}, kv{"format", "date-time"})
	case "date":
		s = append(s, kv{"type", "string"}, kv{"format", "date"})
	case "list":
		item := scalar(f.Of)
		if !g.s.typeNamed(f.Of) {
			if f.Pattern != "" {
				item = append(item, kv{"pattern", f.Pattern})
			}
			if len(f.Enum) > 0 {
				item = append(item, kv{"enum", f.Enum})
			}
		}
		s = append(s, kv{"type", "array"}, kv{"items", item})
	case "object":
		s = append(s, kv{"type", "object"})
	case "map":
		s = append(s, kv{"type", "object"}, kv{"additionalProperties", obj{{"type", f.Of}}})
	default:
		if g.s.typeNamed(f.Type) {
			// A $ref takes no siblings in OpenAPI 3.0: allOf carries the
			// description.
			r := ref("#/components/schemas/" + pascal(f.Type))
			if f.Description == "" {
				return r
			}
			return obj{{"allOf", []any{r}}, {"description", f.Description}}
		}
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
	if f.Ref != "" {
		if target := g.s.byResource(f.Ref); target != nil && g.s.idOf(target) == IDInfrapi {
			s = append(s, kv{"x-infrapi-ref", target.Name})
		}
	}
	if f.Default != nil {
		s = append(s, kv{"default", f.Default})
	}
	if f.Example != nil {
		s = append(s, kv{"example", f.Example})
	}
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
