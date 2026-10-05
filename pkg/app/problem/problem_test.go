package problem

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func render(err error) (int, map[string]any) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(WithCorrelationID(r.Context(), "corr-1"))
	Write(w, r, err)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// A refusal says what it is and whether retrying helps.
func TestARefusalCarriesItsCodeAndRetryability(t *testing.T) {
	code, body := render(InProgress("still running"))
	if code != http.StatusConflict || body["code"] != "in_progress" || body["retryable"] != true ||
		body["correlation_id"] != "corr-1" {
		t.Errorf("%d %v", code, body)
	}
	code, body = render(AlreadyExists(map[string]string{"id": "a"}, "taken"))
	if code != http.StatusConflict || body["retryable"] != false || body["current"] == nil {
		t.Errorf("%d %v", code, body)
	}
}

// An error that is not a refusal is a bug: reported without its text.
func TestAnUnexpectedErrorHidesItsText(t *testing.T) {
	code, body := render(errors.New("dial tcp 10.0.0.1:5432: secret detail"))
	if code != http.StatusInternalServerError || body["code"] != "internal_error" {
		t.Errorf("%d %v", code, body)
	}
	if b, _ := json.Marshal(body); strings.Contains(string(b), "secret detail") {
		t.Errorf("the cause reached the caller: %s", b)
	}
}
