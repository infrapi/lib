package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGinPath(t *testing.T) {
	assert.Equal(t, "/orders/v1/tenants/:tenant/orders/:id", ginPath("/orders/v1/tenants/{tenant}/orders/{id}"))
	assert.Equal(t, "/plain/path", ginPath("/plain/path"))
}

// Mount serves the contract's operations with a net/http handler — the shape
// oapi-codegen generates — each as a route of its own, its parameters set.
func TestMount(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(ordersV1)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders/v1/tenants/{tenant}/orders", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		if _, err := io.WriteString(w, "placed for "+r.PathValue("tenant")); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /orders/v1/tenants/{tenant}/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, r.PathValue("tenant")+"/"+r.PathValue("id")); err != nil {
			t.Error(err)
		}
	})

	gin.SetMode(gin.TestMode)
	e := gin.New()
	(&router{group: e.Group(""), doc: doc}).Mount(mux)

	var routes []string
	for _, ri := range e.Routes() {
		routes = append(routes, ri.Method+" "+ri.Path)
	}
	assert.ElementsMatch(t, []string{
		"POST /orders/v1/tenants/:tenant/orders",
		"GET /orders/v1/tenants/:tenant/orders/:id",
	}, routes, "one route per operation of the contract")

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/orders/v1/tenants/acme/orders", strings.NewReader(`{}`)))
	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "placed for acme", w.Body.String())

	w = httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/orders/v1/tenants/acme/orders/o-1", nil))
	assert.Equal(t, "acme/o-1", w.Body.String())
}
