package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The handlers through the generated router, the way a service tests its own:
// no server, no configuration. The contract's validation is the library's,
// and tested there.
func TestOrders(t *testing.T) {
	h := handlerV1(newOrders(10))
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, "/orders/v1/tenants/acme/orders"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, strings.TrimSpace(w.Body.String())
	}

	for _, step := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{http.MethodPost, "", `{"item":"book"}`, http.StatusCreated, `{"id":"o-1","item":"book","quantity":1}`},
		{http.MethodPut, "/o-1", `{"item":"book","quantity":3}`, http.StatusOK, `{"id":"o-1","item":"book","quantity":3}`},
		{http.MethodPut, "/o-1", `{"item":"book","quantity":11}`, http.StatusBadRequest, `"code":"invalid_request"`},
		{http.MethodGet, "?item=pen", "", http.StatusOK, `[]`},
		{http.MethodDelete, "/o-1", "", http.StatusNoContent, ``},
		{http.MethodDelete, "/o-1", "", http.StatusNoContent, ``},
		{http.MethodGet, "/o-1", "", http.StatusNotFound, `"code":"not_found"`},
	} {
		code, body := do(step.method, step.path, step.body)
		if code != step.code || !strings.Contains(body, step.want) {
			t.Errorf("%s %s: %d %s, want %d containing %s", step.method, step.path, code, body, step.code, step.want)
		}
	}
}
