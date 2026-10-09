package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/infrapi/lib/pkg/app/problem"
)

// IDsExtension, at the root of a contract, names the resource whose ids the
// library keeps for the service: the API answers and takes its own UUIDs, the
// handlers see only the ids of the system behind the service, and the two are
// kept apart, in the service's database.
const IDsExtension = "x-infrapi-ids"

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

// idRoute is what the ids middleware knows of one route.
type idRoute struct {
	idParam   int      // the {id} segment's index in the path, -1 for none
	scope     []string // the path's other parameters
	create    bool     // a POST on the collection
	broadcast bool     // the resource's writes run on every replica: each pairs its own ids
}

// idsMiddleware swaps the API's ids for the backend's on the way in, and back
// on the way out, for a contract with x-infrapi-ids. When the resource's
// writes are broadcast, every replica has its own backend, and its own pairs:
// all the resource's routes, reads too, use this replica's.
func idsMiddleware(store *idStore, resource string, doc *openapi3.T, broadcast func(route string) bool,
	instance func() string) gin.HandlerFunc {
	routes := map[string]idRoute{}
	perReplica := false
	for _, path := range doc.Paths.InMatchingOrder() {
		segs := strings.Split(ginPath(path), "/")
		r := idRoute{idParam: -1}
		for i, s := range segs {
			switch {
			case s == ":id":
				r.idParam = i
			case strings.HasPrefix(s, ":"):
				r.scope = append(r.scope, s[1:])
			}
		}
		for method := range doc.Paths.Value(path).Operations() {
			rr := r
			rr.create = method == http.MethodPost && r.idParam < 0
			key := method + " " + ginPath(path)
			perReplica = perReplica || broadcast(key)
			routes[key] = rr
		}
	}
	for key, r := range routes {
		r.broadcast = perReplica
		routes[key] = r
	}

	return func(c *gin.Context) {
		route, ok := routes[c.Request.Method+" "+c.FullPath()]
		if !ok {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		k := idKey{resource: resource}
		values := make([]string, 0, len(route.scope))
		for _, p := range route.scope {
			values = append(values, c.Param(p))
		}
		k.scope = strings.Join(values, "/")
		if route.broadcast {
			k.instance = instance()
		}

		// In: the API's id becomes the backend's.
		public := ""
		if route.idParam >= 0 {
			public = c.Param("id")
			if _, err := uuid.Parse(public); err != nil {
				problem.Abort(c, problem.Invalid("the id %q is not a UUID", public))
				return
			}
			backend, err := store.backend(ctx, k, public)
			if errors.Is(err, sql.ErrNoRows) {
				problem.Abort(c, problem.NotFound("no %s %q", resource, public))
				return
			}
			if err != nil {
				problem.Abort(c, problem.Unavailable("the ids could not be read: %v", err))
				return
			}
			setPathParam(c, route.idParam, "id", backend)
		}

		held := &heldResponse{ResponseWriter: c.Writer, status: http.StatusOK}
		c.Writer = held
		c.Next()
		c.Writer = held.ResponseWriter

		// Out: the backend's ids become the API's.
		if held.status < http.StatusBadRequest {
			if c.Request.Method == http.MethodDelete && public != "" {
				if err := store.forget(ctx, k, public); err != nil {
					slog.WarnContext(ctx, "ids: a deleted item's id is kept", "resource", resource, "id", public, "err", err)
				}
			}
			proposed := ""
			if route.create {
				proposed = BroadcastUUID(ctx)
			}
			body, err := swapIDs(held.body.Bytes(), func(backend string) (string, error) {
				return store.public(ctx, k, backend, proposed)
			})
			if err != nil {
				problem.Write(c.Writer, c.Request, problem.Unavailable("the ids could not be kept: %v", err))
				return
			}
			held.body.Reset()
			held.body.Write(body)
			held.Header().Del("Content-Length")
		}
		held.send()
	}
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

// swapIDs replaces the "id" of a JSON object, or of each object of a JSON
// list, by what to gives it. Anything else is left as it is.
func swapIDs(body []byte, to func(string) (string, error)) ([]byte, error) {
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
	swap := func(o map[string]any) error {
		id, ok := o["id"].(string)
		if !ok {
			return nil
		}
		public, err := to(id)
		if err != nil {
			return err
		}
		o["id"] = public
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		if err := swap(x); err != nil {
			return nil, err
		}
	case []any:
		for _, item := range x {
			if o, ok := item.(map[string]any); ok {
				if err := swap(o); err != nil {
					return nil, err
				}
			}
		}
	default:
		return body, nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
