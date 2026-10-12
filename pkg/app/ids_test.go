package app

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// telemetryLike has two resources, a reference from one to the other, a list
// of references, a list of objects holding one, a paginated listing and a
// filter on a reference.
var telemetryLike = []byte(`openapi: 3.0.3
info: {title: T, version: "1"}
x-infrapi-ids: t
paths:
  /t/v1/{tenant}/folders:
    parameters:
      - {name: tenant, in: path, required: true, schema: {type: string}}
    get:
      parameters:
        - {name: parent_id, in: query, schema: {type: string, x-infrapi-ref: folders}}
      responses:
        "200":
          description: ok
          content: {application/json: {schema: {$ref: "#/components/schemas/FolderList"}}}
    post:
      requestBody:
        content: {application/json: {schema: {$ref: "#/components/schemas/FolderSpec"}}}
      responses:
        "201":
          description: ok
          content: {application/json: {schema: {$ref: "#/components/schemas/Folder"}}}
  /t/v1/{tenant}/folders/{id}:
    parameters:
      - {name: tenant, in: path, required: true, schema: {type: string}}
      - {name: id, in: path, required: true, schema: {type: string, x-infrapi-id: folders}}
    get:
      responses:
        "200":
          description: ok
          content: {application/json: {schema: {$ref: "#/components/schemas/Folder"}}}
    delete:
      responses:
        "204": {description: gone}
components:
  schemas:
    FolderSpec:
      type: object
      properties:
        name: {type: string}
        parent_id: {type: string, x-infrapi-ref: folders}
        dashboard_ids: {type: array, items: {type: string}, x-infrapi-ref: dashboards}
        teams: {type: array, items: {$ref: "#/components/schemas/Grant"}}
    Folder:
      type: object
      properties:
        id: {type: string, x-infrapi-id: folders}
        name: {type: string}
        parent_id: {type: string, x-infrapi-ref: folders}
        dashboard_ids: {type: array, items: {type: string}, x-infrapi-ref: dashboards}
        teams: {type: array, items: {$ref: "#/components/schemas/Grant"}}
    FolderList:
      type: object
      properties:
        items: {type: array, items: {$ref: "#/components/schemas/Folder"}}
        page: {type: integer}
    Grant:
      type: object
      properties:
        team_id: {type: string, x-infrapi-ref: teams}
        permission: {type: string}
`)

func loadDoc(t *testing.T, content []byte) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(content)
	require.NoError(t, err)
	require.NoError(t, doc.Validate(context.Background()))
	return doc
}

// Every id the schema marks is turned, in objects, lists and lists of
// objects, and nothing else.
func TestMapIDs(t *testing.T) {
	doc := loadDoc(t, telemetryLike)
	to := func(where, resource, id string) (string, error) { return resource + ":" + id, nil }
	out, err := mapJSON([]byte(`{"items":[{"id":"f1","name":"id","parent_id":"f0","dashboard_ids":["d1","d2"],
		"teams":[{"team_id":"12","permission":"View"}]}],"page":1}`), doc.Components.Schemas["FolderList"].Value, to)
	require.NoError(t, err)
	assert.JSONEq(t, `{"items":[{"id":"folders:f1","name":"id","parent_id":"folders:f0",
		"dashboard_ids":["dashboards:d1","dashboards:d2"],"teams":[{"team_id":"teams:12","permission":"View"}]}],"page":1}`,
		string(out))

	var fields []string
	_, err = mapJSON([]byte(`{"parent_id":"x","teams":[{"team_id":"y"}]}`), doc.Components.Schemas["FolderSpec"].Value,
		func(where, _, id string) (string, error) { fields = append(fields, where); return id, nil })
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"parent_id", "teams.team_id"}, fields, "named for the caller")

	_, err = mapJSON([]byte(`{"parent_id":"x"}`), doc.Components.Schemas["FolderSpec"].Value,
		func(string, string, string) (string, error) { return "", fmt.Errorf("db down") })
	assert.ErrorContains(t, err, "db down")

	for _, in := range []string{``, `not json`} {
		same, err := mapJSON([]byte(in), doc.Components.Schemas["Folder"].Value, to)
		require.NoError(t, err)
		assert.Equal(t, in, string(same))
	}
}

// A contract that names its resource at the root alone keeps working: its
// {id} and its objects' ids are that resource's.
func TestMarkLegacy(t *testing.T) {
	doc := loadDoc(t, []byte(`openapi: 3.0.3
info: {title: B, version: "1"}
x-infrapi-ids: bookmarks
paths:
  /b/{id}:
    get:
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses: {"200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Bookmark"}}}}}
components:
  schemas:
    Bookmark: {type: object, properties: {id: {type: string}, title: {type: string}}}
`))
	require.NoError(t, markLegacy(doc))
	assert.Equal(t, "bookmarks", marked(doc.Components.Schemas["Bookmark"].Value.Properties["id"].Value, IDExtension))
	param := doc.Paths.Value("/b/{id}").Get.Parameters[0].Value
	assert.Equal(t, "bookmarks", marked(param.Schema.Value, IDExtension))
}

// memPairs keeps the pairs in memory, as the table does.
type memPairs struct {
	mu     sync.Mutex
	byPub  map[idKey]map[string]string
	byBack map[idKey]map[string]string
	next   int
}

func newMemPairs() *memPairs {
	return &memPairs{byPub: map[idKey]map[string]string{}, byBack: map[idKey]map[string]string{}}
}

func (m *memPairs) backend(_ context.Context, k idKey, public string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.byPub[k][public]; ok {
		return b, nil
	}
	return "", sql.ErrNoRows
}

func (m *memPairs) public(_ context.Context, k idKey, backend, proposed string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.byBack[k][backend]; ok {
		return p, nil
	}
	if proposed == "" {
		m.next++
		proposed = fmt.Sprintf("00000000-0000-4000-8000-%012d", m.next)
	}
	if m.byPub[k] == nil {
		m.byPub[k], m.byBack[k] = map[string]string{}, map[string]string{}
	}
	m.byPub[k][proposed], m.byBack[k][backend] = backend, proposed
	return proposed, nil
}

func (m *memPairs) forget(_ context.Context, k idKey, public string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byBack[k], m.byPub[k][public])
	delete(m.byPub[k], public)
	return nil
}

// Through the middleware: the handlers see the backend's ids only, the
// caller the API's only, a reference included.
func TestIDsMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doc := loadDoc(t, telemetryLike)
	pairs := newMemPairs()
	mw, err := idsMiddleware(pairs, doc, func(string) bool { return false }, func() string { return "" })
	require.NoError(t, err)

	var seen []string // what the handlers were given
	e := gin.New()
	g := e.Group("", mw)
	g.POST("/t/v1/:tenant/folders", func(c *gin.Context) {
		b, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		if strings.Contains(string(b), `"dup"`) {
			c.Data(http.StatusConflict, "application/problem+json",
				[]byte(`{"status":409,"code":"already_exists","current":{"id":"uid-root","name":"root"}}`))
			return
		}
		seen = append(seen, string(b))
		parent := ""
		if strings.Contains(string(b), "uid-root") {
			parent = `,"parent_id":"uid-root"`
		}
		c.Data(http.StatusCreated, "application/json", []byte(`{"id":"uid-`+fmt.Sprint(len(seen))+`","name":"n"`+parent+`}`))
	})
	g.GET("/t/v1/:tenant/folders/:id", func(c *gin.Context) {
		seen = append(seen, c.Param("id"))
		c.JSON(http.StatusOK, map[string]any{"id": c.Param("id"), "teams": []any{map[string]any{"team_id": "12"}}})
	})
	g.GET("/t/v1/:tenant/folders", func(c *gin.Context) {
		seen = append(seen, c.Query("parent_id"))
		c.JSON(http.StatusOK, map[string]any{"items": []any{map[string]any{"id": "uid-made-outside"}}, "page": 1})
	})
	g.DELETE("/t/v1/:tenant/folders/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	do := func(method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	idOf := func(body string) string {
		i := strings.Index(body, `"id":"`) + 6
		return body[i : i+36]
	}

	code, body := do(http.MethodPost, "/t/v1/acme/folders", `{"name":"root"}`)
	require.Equal(t, http.StatusCreated, code, body)
	root := idOf(body)
	assert.NotContains(t, body, "uid-1")

	pairs.mu.Lock()
	pairs.byPub[idKey{resource: "folders", scope: "acme"}][root] = "uid-root" // as if the backend had named it so
	pairs.byBack[idKey{resource: "folders", scope: "acme"}]["uid-root"] = root
	pairs.mu.Unlock()

	code, body = do(http.MethodPost, "/t/v1/acme/folders", `{"name":"child","parent_id":"`+root+`"}`)
	require.Equal(t, http.StatusCreated, code, body)
	assert.Contains(t, seen[1], `"parent_id":"uid-root"`, "the handler gets the backend's id")
	assert.Contains(t, body, `"parent_id":"`+root+`"`, "the caller gets the API's")

	code, body = do(http.MethodGet, "/t/v1/acme/folders/"+root, "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "uid-root", seen[2])
	assert.NotContains(t, body, `"12"`, "a team met for the first time is given an id")

	code, body = do(http.MethodGet, "/t/v1/acme/folders?parent_id="+root, "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "uid-root", seen[3], "a filter on a reference too")
	assert.NotContains(t, body, "uid-made-outside")

	code, body = do(http.MethodGet, "/t/v1/acme/folders/not-a-uuid", "")
	assert.Equal(t, http.StatusBadRequest, code, body)
	code, _ = do(http.MethodGet, "/t/v1/acme/folders/00000000-0000-4000-8000-999999999999", "")
	assert.Equal(t, http.StatusNotFound, code)
	code, body = do(http.MethodPost, "/t/v1/acme/folders", `{"name":"x","parent_id":"00000000-0000-4000-8000-999999999999"}`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "parent_id: no folders")
	code, _ = do(http.MethodGet, "/t/v1/other/folders/"+root, "")
	assert.Equal(t, http.StatusNotFound, code, "another tenant's ids are not this one's")

	code, body = do(http.MethodPost, "/t/v1/acme/folders", `{"name":"dup"}`)
	require.Equal(t, http.StatusConflict, code, body)
	assert.Contains(t, body, `"current":{"id":"`+root+`"`, "a conflict's current, with the API's id")

	code, _ = do(http.MethodDelete, "/t/v1/acme/folders/"+root, "")
	require.Equal(t, http.StatusNoContent, code)
	code, _ = do(http.MethodGet, "/t/v1/acme/folders/"+root, "")
	assert.Equal(t, http.StatusNotFound, code, "a deleted item's id is forgotten")
	code, _ = do(http.MethodDelete, "/t/v1/acme/folders/"+root, "")
	assert.Equal(t, http.StatusNoContent, code, "deleted again: already gone, as the contract answers")
}

// The handler reads the backend's id from gin's parameters and from the URL.
func TestSetPathParam(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/bookmarks/v1/tenants/acme/bookmarks/2d1f6c1e-uuid", nil)
	c.Params = gin.Params{{Key: "tenant", Value: "acme"}, {Key: "id", Value: "2d1f6c1e-uuid"}}
	setPathParam(c, 6, "id", "bm-1")
	assert.Equal(t, "bm-1", c.Param("id"))
	assert.Equal(t, "/bookmarks/v1/tenants/acme/bookmarks/bm-1", c.Request.URL.Path)
}
