package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/events"
)

var vmsV1 = OpenAPISpec{Name: "compute v1", Path: "compute/v1.yaml", Content: []byte(`openapi: 3.0.3
info: {title: compute, version: "1"}
paths:
  /apis/test.infrapi.io/v1/namespaces/{namespace}/vms:
    parameters: [{name: namespace, in: path, required: true, schema: {type: string}}]
    post: {operationId: createVM, responses: {"201": {description: ok}}}
  /apis/test.infrapi.io/v1/namespaces/{namespace}/vms/{vm}:
    parameters:
      - {name: namespace, in: path, required: true, schema: {type: string}}
      - {name: vm, in: path, required: true, schema: {type: string}}
    put: {operationId: replaceVM, responses: {"200": {description: ok}}}
    delete: {operationId: deleteVM, x-infrapi-event: false, responses: {"204": {description: ok}}}
  /apis/test.infrapi.io/v1/namespaces/{namespace}/vms/{vm}/reboot:
    parameters:
      - {name: namespace, in: path, required: true, schema: {type: string}}
      - {name: vm, in: path, required: true, schema: {type: string}}
    post: {operationId: rebootVM, responses: {"202": {description: ok}}}
  /apis/test.infrapi.io/v1/namespaces/{namespace}/vms/{vm}/promote:
    parameters:
      - {name: namespace, in: path, required: true, schema: {type: string}}
      - {name: vm, in: path, required: true, schema: {type: string}}
    post: {operationId: promoteVM, x-infrapi-event: {reason: ReleasePromoted}, responses: {"200": {description: ok}}}
`)}

type memSender struct {
	mu  sync.Mutex
	evs []events.Event
}

func (m *memSender) Create(_ context.Context, _ string, evs []events.Event) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, evs...)
	return make([]string, len(evs)), nil
}

func (m *memSender) UpdateSeries(context.Context, string, string, events.Series) error { return nil }

// Every write served gets its event, after its handler: its reason from the
// operation, Accepted for a 202, Failed and the error's detail for a
// failure; none for a read, a request the check refused, an operation that
// turns it off, a handler that skips it.
func TestWithEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sent := &memSender{}
	rec := events.NewRecorder(events.Options{Controller: "compute", Sender: sent, BatchWait: time.Millisecond})
	// the check: X-User passes it, as pkg/auth's RequireAccess does
	check := func(c *gin.Context) {
		if u := c.GetHeader("X-User"); u != "" {
			c.Set(CheckedUserKey, u)
			c.Next()
			return
		}
		AbortError(c, infraerrors.New(http.StatusForbidden, "t", "denied"))
	}
	app, err := NewServerGin(testAppConfig(t), []string{testRoot}, WithOpenAPI(vmsV1), WithEvents(rec, EventHooks{
		After: func(c *gin.Context, e *events.Event) { e.Details = map[string]any{"hook": true} },
	}))
	require.NoError(t, err)
	g := app.Group(testRoot+"/namespaces/:namespace", check)
	g.POST("/vms", func(c *gin.Context) {
		if c.Query("fail") != "" {
			AbortError(c, infraerrors.New(http.StatusConflict, "t", "web-1 exists"))
			return
		}
		events.SetNote(c, "on host h-12")
		c.Status(http.StatusCreated)
	})
	g.PUT("/vms/:vm", func(c *gin.Context) {
		if c.Query("skip") != "" {
			events.Skip(c)
		}
		c.Status(http.StatusOK)
	})
	g.DELETE("/vms/:vm", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	g.POST("/vms/:vm/reboot", func(c *gin.Context) { c.Status(http.StatusAccepted) })
	g.POST("/vms/:vm/promote", func(c *gin.Context) { c.Status(http.StatusOK) })
	g.GET("/vms", func(c *gin.Context) { c.Status(http.StatusOK) })

	jdoe := map[string]string{"X-User": "jdoe", "X-Request-Id": "req-1"}
	base := testRoot + "/namespaces/core/vms"
	call(t, app, http.MethodPost, base, `{"name":"web-1"}`, jdoe)
	call(t, app, http.MethodPost, base+"?fail=1", `{"name":"web-1"}`, jdoe)
	call(t, app, http.MethodPost, base+"/web-1/reboot", "", jdoe)
	call(t, app, http.MethodPost, base+"/web-1/promote", "", jdoe)
	call(t, app, http.MethodPut, base+"/web-1", "", jdoe)
	call(t, app, http.MethodPut, base+"/web-2?skip=1", "", jdoe) // another object: an event of its own, but skipped
	call(t, app, http.MethodDelete, base+"/web-1", "", jdoe)
	call(t, app, http.MethodGet, base, "", jdoe)
	call(t, app, http.MethodPost, base, "", nil) // refused by the check
	require.NoError(t, rec.Close(context.Background()))

	sent.mu.Lock()
	defer sent.mu.Unlock()
	var got []string
	for _, e := range sent.evs {
		got = append(got, e.Type+" "+e.Reason+" "+e.Regarding.Name+" "+e.Note)
	}
	assert.Equal(t, []string{
		"Normal VMCreated web-1 on host h-12",
		"Warning VMCreateFailed web-1 web-1 exists",
		"Normal VMRebootAccepted web-1 ",
		"Normal ReleasePromoted web-1 ",
		"Normal VMReplaced web-1 ",
	}, got)
	e := sent.evs[0]
	assert.Equal(t, events.Regarding{API: "test.infrapi.io", Namespace: "core", Resource: "vms", Name: "web-1"}, e.Regarding, "a create's name, from its body")
	assert.Equal(t, "jdoe", e.User)
	assert.Equal(t, "req-1", e.RequestID)
	assert.Equal(t, "create", e.Action)
	assert.Equal(t, "compute", e.ReportingController)
	assert.Equal(t, map[string]any{"hook": true}, e.Details)
}

func TestPast(t *testing.T) {
	for verb, want := range map[string]string{"create": "Created", "reboot": "Rebooted", "replace": "Replaced", "deploy": "Deployed", "apply": "Applied", "patch": "Patched"} {
		assert.Equal(t, want, past(verb), verb)
	}
}
