package server

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

func render(err error) (int, map[string]any) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(WithCorrelationID(r.Context(), "corr-1"))
	WriteError(w, r, err)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		panic(err) // WriteError always writes a document
	}
	return w.Code, body
}

// A refusal says what it is and whether retrying helps.
func TestWriteError_refusalCarriesItsCodeAndRetryability(t *testing.T) {
	code, body := render(infraerrors.Unavailable("still running"))
	if code != http.StatusServiceUnavailable || body["code"] != "upstream_unavailable" || body["retryable"] != true ||
		body["correlation_id"] != "corr-1" {
		t.Errorf("%d %v", code, body)
	}
	code, body = render(infraerrors.AlreadyExists(map[string]string{"id": "a"}, "taken"))
	if code != http.StatusConflict || body["retryable"] != false || body["current"] == nil {
		t.Errorf("%d %v", code, body)
	}
}

// An error that is not *infraerrors.Error is a bug: reported without its text.
func TestWriteError_unexpectedErrorHidesItsText(t *testing.T) {
	code, body := render(stderrors.New("dial tcp 10.0.0.1:5432: secret detail"))
	if code != http.StatusInternalServerError || body["code"] != "internal_error" {
		t.Errorf("%d %v", code, body)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret detail") {
		t.Errorf("the cause reached the caller: %s", b)
	}
}

// An error that leaves Retryable unset — a bare New or Wrap — still answers
// "retryable": false: the client always gets an explicit flag, even when the
// error itself did not bother to set one.
func TestWriteError_unsetRetryableAnswersFalse(t *testing.T) {
	_, body := render(infraerrors.New(http.StatusBadGateway, "config.Read", "upstream refused the read"))
	if body["retryable"] != false {
		t.Errorf("%v", body)
	}
}
