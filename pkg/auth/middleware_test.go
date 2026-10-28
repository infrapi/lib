package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	app.Any("/api/v1/namespaces/:namespace/stacks/:stack/*resource", client.RequireAccess(), func(c *gin.Context) {
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/core/stacks/compute/vm", nil)
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

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/namespaces/core/stacks/compute/vm/123", nil)
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
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/core/stacks/compute/vm", nil))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
