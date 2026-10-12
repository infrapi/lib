package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/infrapi/lib/pkg/app/problem"
)

// The extensions with which a contract has the library keep its ids: the API
// answers and takes its own UUIDs, the handlers see only the ids of the
// system behind the service, and the two are kept apart, in the service's
// database.
const (
	// IDsExtension, at the root of a contract, turns it on. Its value names
	// the resource of a contract that marks nothing else: its {id} and the id
	// of its objects are that resource's.
	IDsExtension = "x-infrapi-ids"
	// IDExtension, on a schema or a path parameter's schema, says the value is
	// the id of an item of the named resource: its own.
	IDExtension = "x-infrapi-id"
	// RefExtension, on a schema, says the value — or each item of the list —
	// is the id of an item of the named resource: a reference to it.
	RefExtension = "x-infrapi-ref"
)

// idStore is the table that pairs the API's ids with the backend's.
type idStore struct{ db *sql.DB }

func newIDStore(ctx context.Context, db *sql.DB) (*idStore, error) {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS infrapi_ids (
    resource   text        NOT NULL,
    scope      text        NOT NULL,
    instance   text        NOT NULL,
    public_id  uuid        NOT NULL,
    backend_id text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource, scope, instance, public_id),
    UNIQUE (resource, scope, instance, backend_id)
)`)
	if err != nil {
		return nil, fmt.Errorf("app: ids table: %w", err)
	}
	return &idStore{db: db}, nil
}

// key is where a pair lives: the resource, its scope — the path's other
// parameters, as a tenant — and, for a broadcast write, the replica, whose
// backend gives its own ids.
type idKey struct{ resource, scope, instance string }

func (s *idStore) backend(ctx context.Context, k idKey, public string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT backend_id FROM infrapi_ids
WHERE resource = $1 AND scope = $2 AND instance = $3 AND public_id = $4`, k.resource, k.scope, k.instance, public).Scan(&id)
	return id, err
}

// public is the API's id for a backend id, given one when it has none: an
// item the service creates gets the write's (proposed), one it meets for the
// first time — made outside the API — a random one.
func (s *idStore) public(ctx context.Context, k idKey, backend, proposed string) (string, error) {
	if proposed == "" {
		proposed = uuid.NewString()
	}
	var id string
	err := s.db.QueryRowContext(ctx, `INSERT INTO infrapi_ids (resource, scope, instance, public_id, backend_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (resource, scope, instance, backend_id) DO UPDATE SET backend_id = EXCLUDED.backend_id
RETURNING public_id`, k.resource, k.scope, k.instance, proposed, backend).Scan(&id)
	return id, err
}

func (s *idStore) forget(ctx context.Context, k idKey, public string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM infrapi_ids
WHERE resource = $1 AND scope = $2 AND instance = $3 AND public_id = $4`, k.resource, k.scope, k.instance, public)
	return err
}

// idPairs is where the pairs are kept: the database's table, or a test's.
type idPairs interface {
	backend(ctx context.Context, k idKey, public string) (string, error)
	public(ctx context.Context, k idKey, backend, proposed string) (string, error)
	forget(ctx context.Context, k idKey, public string) error
}

// idRoute is what the ids middleware knows of one route.
type idRoute struct {
	resource string   // the {id}'s resource; empty for none
	idParam  int      // the {id} segment's index in the path, -1 for none
	scope    []string // the path's other parameters
	create   bool     // a POST on a collection
	query    map[string]string
	body     *openapi3.Schema
	answers  map[int]*openapi3.Schema // the JSON answers, by status
	item     *openapi3.Schema         // the item the route answers: a problem's current is one
	// created is the resource a create makes: the one its answer's id is.
	created string
	// goneIsDone: a delete whose contract answers 204 and never 404 — gone or
	// already gone — answers 204 for an id it does not know: one it
	// forgot when it deleted it, for a retried delete.
	goneIsDone bool
}

// idsMiddleware swaps the API's ids for the backend's on the way in — the
// path's {id}, the filters, the body — and back on the way out, wherever the
// contract marks an id (x-infrapi-id) or a reference (x-infrapi-ref). When
// the writes are broadcast, every replica has its own backend, and its own
// pairs: all the routes, reads too, use this replica's.
func idsMiddleware(store idPairs, doc *openapi3.T, broadcast func(route string) bool,
	instance func() string) (gin.HandlerFunc, error) {
	if err := markLegacy(doc); err != nil {
		return nil, err
	}
	routes := map[string]idRoute{}
	perReplica := false
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		segs := strings.Split(ginPath(path), "/")
		for method, op := range item.Operations() {
			r := idRoute{idParam: -1, query: map[string]string{}, answers: map[int]*openapi3.Schema{}}
			params := append(paramsOf(item.Parameters), paramsOf(op.Parameters)...)
			for i, s := range segs {
				switch {
				case s == ":id":
					r.idParam = i
				case strings.HasPrefix(s, ":"):
					r.scope = append(r.scope, s[1:])
				}
			}
			for _, p := range params {
				if p == nil || p.Schema == nil || p.Schema.Value == nil {
					continue
				}
				switch {
				case p.In == openapi3.ParameterInPath && p.Name == "id":
					r.resource = marked(p.Schema.Value, IDExtension)
				case p.In == openapi3.ParameterInQuery:
					if res := marked(p.Schema.Value, RefExtension, IDExtension); res != "" {
						r.query[p.Name] = res
					}
				}
			}
			if r.idParam >= 0 && r.resource == "" {
				return nil, fmt.Errorf("app: %s %s: the {id} names no resource (%s on its schema)", method, path, IDExtension)
			}
			r.create = method == http.MethodPost && r.idParam < 0
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				r.body = jsonSchema(op.RequestBody.Value.Content)
			}
			if op.Responses != nil {
				r.goneIsDone = method == http.MethodDelete && op.Responses.Value("204") != nil &&
					op.Responses.Value("404") == nil
				for code, resp := range op.Responses.Map() {
					status, err := strconv.Atoi(code)
					if err != nil || resp.Value == nil {
						continue
					}
					if s := jsonSchema(resp.Value.Content); s != nil {
						r.answers[status] = s
						if status < 300 && r.item == nil {
							r.item = s
						}
					}
				}
			}
			if r.create {
				r.created = marked(property(r.item, "id"), IDExtension)
			}
			key := method + " " + ginPath(path)
			perReplica = perReplica || broadcast(key)
			routes[key] = r
		}
	}

	return func(c *gin.Context) {
		route, ok := routes[c.Request.Method+" "+c.FullPath()]
		if !ok {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		values := make([]string, 0, len(route.scope))
		for _, p := range route.scope {
			values = append(values, c.Param(p))
		}
		scope, inst := strings.Join(values, "/"), ""
		if perReplica {
			inst = instance()
		}
		key := func(resource string) idKey { return idKey{resource: resource, scope: scope, instance: inst} }

		// In: the API's ids become the backend's.
		in := func(where, resource, public string) (string, error) {
			if _, err := uuid.Parse(public); err != nil {
				return "", problem.Invalid("%s: %q is not a UUID", where, public)
			}
			backend, err := store.backend(ctx, key(resource), public)
			if errors.Is(err, sql.ErrNoRows) {
				if where == "id" {
					return "", problem.NotFound("%q is not the id of one of the %s", public, resource)
				}
				return "", problem.Invalid("%s: %q is not the id of one of the %s", where, public, resource)
			}
			if err != nil {
				return "", problem.Unavailable("the ids could not be read: %v", err)
			}
			return backend, nil
		}
		public := ""
		if route.idParam >= 0 {
			public = c.Param("id")
			backend, err := in("id", route.resource, public)
			var pe *problem.Error
			if route.goneIsDone && errors.As(err, &pe) && pe.Status == http.StatusNotFound {
				c.AbortWithStatus(http.StatusNoContent) // already gone
				return
			}
			if err != nil {
				problem.Abort(c, err)
				return
			}
			setPathParam(c, route.idParam, "id", backend)
		}
		if len(route.query) > 0 {
			q := c.Request.URL.Query()
			for name, resource := range route.query {
				for i, v := range q[name] {
					backend, err := in(name, resource, v)
					if err != nil {
						problem.Abort(c, err)
						return
					}
					q[name][i] = backend
				}
			}
			c.Request.URL.RawQuery = q.Encode()
		}
		if route.body != nil && c.Request.Body != nil {
			raw, err := io.ReadAll(c.Request.Body)
			if err != nil {
				problem.Abort(c, problem.Invalid("the body could not be read: %v", err))
				return
			}
			out, err := mapJSON(raw, route.body, func(where, resource, v string) (string, error) {
				return in(where, resource, v)
			})
			if err != nil {
				problem.Abort(c, err)
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(out))
			c.Request.ContentLength = int64(len(out))
		}

		held := &heldResponse{ResponseWriter: c.Writer, status: http.StatusOK}
		c.Writer = held
		c.Next()
		c.Writer = held.ResponseWriter

		// Out: the backend's ids become the API's.
		if held.status < http.StatusBadRequest && c.Request.Method == http.MethodDelete && public != "" {
			if err := store.forget(ctx, key(route.resource), public); err != nil {
				slog.WarnContext(ctx, "ids: a deleted item's id is kept", "resource", route.resource, "id", public, "err", err)
			}
		}
		proposed := ""
		if route.create && held.status < http.StatusBadRequest {
			proposed = BroadcastUUID(ctx)
		}
		out := func(where, resource, backend string) (string, error) {
			p := ""
			if where == "id" && resource == route.created {
				p = proposed // what the create made: its id is the write's
			}
			return store.public(ctx, key(resource), backend, p)
		}
		schema := route.answers[held.status]
		if held.status >= http.StatusBadRequest && route.item != nil {
			// A problem may carry the item as it is: a conflict's current.
			schema = &openapi3.Schema{Properties: openapi3.Schemas{"current": {Value: route.item}}}
		}
		if schema != nil {
			body, err := mapJSON(held.body.Bytes(), schema, out)
			if err != nil {
				problem.Write(c.Writer, c.Request, problem.Unavailable("the ids could not be kept: %v", err))
				return
			}
			held.body.Reset()
			held.body.Write(body)
			held.Header().Del("Content-Length")
		}
		held.send()
	}, nil
}

// paramsOf turns OpenAPI's parameter references into the parameters.
func paramsOf(ps openapi3.Parameters) []*openapi3.Parameter {
	out := make([]*openapi3.Parameter, 0, len(ps))
	for _, p := range ps {
		if p != nil {
			out = append(out, p.Value)
		}
	}
	return out
}

// jsonSchema is the schema of a body's JSON, nil for none.
func jsonSchema(content openapi3.Content) *openapi3.Schema {
	for _, mt := range []string{"application/json", "application/problem+json"} {
		if m := content.Get(mt); m != nil && m.Schema != nil {
			return m.Schema.Value
		}
	}
	return nil
}

// marked is the resource a schema names in the first of the extensions it
// has, its allOf's too; empty for none.
func marked(s *openapi3.Schema, extensions ...string) string {
	if s == nil {
		return ""
	}
	for _, ext := range extensions {
		if v, ok := s.Extensions[ext].(string); ok && v != "" {
			return v
		}
	}
	for _, sub := range s.AllOf {
		if res := marked(sub.Value, extensions...); res != "" {
			return res
		}
	}
	return ""
}

// property is an object schema's property, its allOf's too.
func property(s *openapi3.Schema, name string) *openapi3.Schema {
	if s == nil {
		return nil
	}
	if p, ok := s.Properties[name]; ok && p != nil {
		return p.Value
	}
	for _, sub := range s.AllOf {
		if p := property(sub.Value, name); p != nil {
			return p
		}
	}
	return nil
}

// items is a list schema's items, its allOf's too.
func items(s *openapi3.Schema) *openapi3.Schema {
	if s == nil {
		return nil
	}
	if s.Items != nil {
		return s.Items.Value
	}
	for _, sub := range s.AllOf {
		if it := items(sub.Value); it != nil {
			return it
		}
	}
	return nil
}

// idMapper turns one id of the named resource into the other side's; where
// is the field, for the caller's sake.
type idMapper func(where, resource, id string) (string, error)

// mapJSON turns every id the schema marks in a JSON body. A body that is not
// JSON is left as it is.
func mapJSON(body []byte, s *openapi3.Schema, to idMapper) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return body, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return body, nil //nolint:nilerr // not JSON after all: sent as it is
	}
	changed, err := mapIDs(v, s, "", to)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(changed)
	if err != nil {
		return nil, err
	}
	if body[len(body)-1] == '\n' {
		out = append(out, '\n')
	}
	return out, nil
}

// mapIDs walks a decoded JSON value with its schema.
func mapIDs(v any, s *openapi3.Schema, where string, to idMapper) (any, error) {
	if s == nil {
		return v, nil
	}
	resource := marked(s, IDExtension, RefExtension)
	switch x := v.(type) {
	case string:
		if resource == "" {
			return x, nil
		}
		return to(where, resource, x)
	case []any:
		item := items(s)
		for i, e := range x {
			// A list marked as a whole: each of its strings is an id.
			sub := item
			if resource != "" && marked(item, IDExtension, RefExtension) == "" {
				sub = &openapi3.Schema{Extensions: map[string]any{RefExtension: resource}}
			}
			m, err := mapIDs(e, sub, where, to)
			if err != nil {
				return nil, err
			}
			x[i] = m
		}
		return x, nil
	case map[string]any:
		for k, e := range x {
			name := k
			if where != "" {
				name = where + "." + k
			}
			m, err := mapIDs(e, property(s, k), name, to)
			if err != nil {
				return nil, err
			}
			x[k] = m
		}
		return x, nil
	}
	return v, nil
}

// markLegacy marks a contract that names its resource at the root alone
// (x-infrapi-ids: bookmarks) the way a marked one is: its {id}, and the id
// of every object of its components, are that resource's.
func markLegacy(doc *openapi3.T) error {
	root, set, err := extension(doc.Extensions, IDsExtension)
	if err != nil {
		return err
	}
	if !set || anyMarked(doc) {
		return nil
	}
	mark := func(s *openapi3.Schema) {
		if s.Extensions == nil {
			s.Extensions = map[string]any{}
		}
		s.Extensions[IDExtension] = root
	}
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		ps := paramsOf(item.Parameters)
		for _, op := range item.Operations() {
			ps = append(ps, paramsOf(op.Parameters)...)
		}
		for _, p := range ps {
			if p.In == openapi3.ParameterInPath && p.Name == "id" && p.Schema != nil && p.Schema.Value != nil {
				mark(p.Schema.Value)
			}
		}
	}
	if doc.Components != nil {
		for _, s := range doc.Components.Schemas {
			if s.Value == nil {
				continue
			}
			if id := s.Value.Properties["id"]; id != nil && id.Value != nil {
				mark(id.Value)
			}
		}
	}
	return nil
}

// anyMarked says whether the contract marks an id anywhere.
func anyMarked(doc *openapi3.T) bool {
	found := false
	var walk func(s *openapi3.Schema, seen map[*openapi3.Schema]bool)
	walk = func(s *openapi3.Schema, seen map[*openapi3.Schema]bool) {
		if s == nil || seen[s] || found {
			return
		}
		seen[s] = true
		if marked(s, IDExtension, RefExtension) != "" {
			found = true
			return
		}
		for _, p := range s.Properties {
			walk(p.Value, seen)
		}
		if s.Items != nil {
			walk(s.Items.Value, seen)
		}
		for _, sub := range s.AllOf {
			walk(sub.Value, seen)
		}
	}
	seen := map[*openapi3.Schema]bool{}
	if doc.Components != nil {
		for _, s := range doc.Components.Schemas {
			walk(s.Value, seen)
		}
		for _, p := range doc.Components.Parameters {
			if p.Value != nil && p.Value.Schema != nil {
				walk(p.Value.Schema.Value, seen)
			}
		}
	}
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		ps := paramsOf(item.Parameters)
		for _, op := range item.Operations() {
			ps = append(ps, paramsOf(op.Parameters)...)
		}
		for _, p := range ps {
			if p.Schema != nil {
				walk(p.Schema.Value, seen)
			}
		}
	}
	return found
}

// setPathParam puts the backend's id where the handler reads it: gin's
// parameters, and the URL a mounted net/http handler routes on.
func setPathParam(c *gin.Context, index int, name, value string) {
	for i := range c.Params {
		if c.Params[i].Key == name {
			c.Params[i].Value = value
		}
	}
	segs := strings.Split(c.Request.URL.Path, "/")
	if index < len(segs) {
		segs[index] = value
		c.Request.URL.Path = strings.Join(segs, "/")
		c.Request.URL.RawPath = ""
	}
}
