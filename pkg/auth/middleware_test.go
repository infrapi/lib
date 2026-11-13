package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/api"
	"github.com/infrapi/lib/pkg/server"
)

func init() { gin.SetMode(gin.TestMode) }

func testEngine(t *testing.T, client *Client) *gin.Engine {
	t.Helper()
	app := gin.New()
	app.GET("/widgets", client.Middleware(), func(c *gin.Context) {
		user, ok := GetUser(c)
		require.True(t, ok)
		c.JSON(http.StatusOK, gin.H{"sub": user.Subject})
	})
	return app
}

func TestMiddleware_authenticates(t *testing.T) {
	authSvc := authStub(t, "good-token", `{"sub":"jdoe","provider":"okta","authorization":"idm"}`)
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := testEngine(t, client)

	req := httptest.NewRequest(http.MethodGet, "/widgets", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"sub":"jdoe"}`, w.Body.String())
}

func TestMiddleware_missingHeader(t *testing.T) {
	authSvc := authStub(t, "good-token", `{}`)
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := testEngine(t, client)

	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/widgets", nil))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_tokenRejected(t *testing.T) {
	authSvc := authStub(t, "good-token", `{}`)
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := testEngine(t, client)

	req := httptest.NewRequest(http.MethodGet, "/widgets", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestGetUser_notSet(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	_, ok := GetUser(c)
	assert.False(t, ok)
}

func testAccessEngine(t *testing.T, client *Client) *gin.Engine {
	t.Helper()
	app := gin.New()
	app.Any("/apis/:api/v1/namespaces/:namespace/*resource", client.RequireAccess(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return app
}

func TestRequireAccess_allowed(t *testing.T) {
	authSvc := authorizeStub(t, "good-token", `{"allowed":true}`)
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := testAccessEngine(t, client)

	req := httptest.NewRequest(http.MethodGet, "/apis/compute.infrapi.io/v1/namespaces/core/vm", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequireAccess_denied(t *testing.T) {
	authSvc := authorizeStub(t, "good-token", `{"allowed":false,"reason":"nope"}`)
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := testAccessEngine(t, client)

	req := httptest.NewRequest(http.MethodDelete, "/apis/compute.infrapi.io/v1/namespaces/core/vm/123", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestRequireAccess_missingHeader(t *testing.T) {
	authSvc := authorizeStub(t, "good-token", `{"allowed":true}`)
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := testAccessEngine(t, client)

	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/apis/compute.infrapi.io/v1/namespaces/core/vm", nil))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestRequireAccess_apiGroup checks RequireAccess on routes mounted by
// server.APIGroup, with named parameters rather than a wildcard, and what it
// asks the authorization service.
func TestRequireAccess_apiGroup(t *testing.T) {
	var got AuthorizeRequest
	authSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"allowed":true}`))
	}))
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := gin.New()
	vms, err := server.APIGroup(app, api.Group{Name: "compute", Domain: "infrapi.io", Version: "v2beta1"})
	require.NoError(t, err)
	vms.Use(client.RequireAccess())
	vms.POST("/vm/:id/reboot", func(c *gin.Context) { c.Status(http.StatusAccepted) })

	req := httptest.NewRequest(http.MethodPost, "/apis/compute.infrapi.io/v2beta1/namespaces/core/vm/123/reboot", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, AuthorizeRequest{
		Namespace: "core", Api: "compute.infrapi.io", Resource: "vm/reboot", Verb: "create",
	}, got)
}

func TestRequireAccess_outsideLayout(t *testing.T) {
	authSvc := authorizeStub(t, "good-token", `{"allowed":true}`)
	defer authSvc.Close()

	client, err := NewClient(ClientOptions{BaseURL: authSvc.URL})
	require.NoError(t, err)

	app := gin.New()
	app.GET("/widgets", client.RequireAccess(), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/widgets", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
