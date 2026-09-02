package errors

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestError_message(t *testing.T) {
	err := New(http.StatusNotFound, "user.Get", "no user with id %d", 42)

	assert.Equal(t,
		"infrapi-error: status=404 | title=Not Found | origin=user.Get | detail=no user with id 42",
		err.Error())
}

func TestError_allMembers(t *testing.T) {
	err := New(http.StatusConflict, "subscription.Create", "id %q is taken", "sub-1").
		WithType("https://infrapi.example.com/errors/duplicate", "Duplicate subscription").
		WithSolution("pick another id")
	err.Instance = "/subscriptions/sub-1"

	assert.Equal(t,
		"infrapi-error: status=409 | title=Duplicate subscription"+
			" | type=https://infrapi.example.com/errors/duplicate"+
			" | origin=subscription.Create | detail=id \"sub-1\" is taken"+
			" | instance=/subscriptions/sub-1 | solution=pick another id",
		err.Error())
}

func TestError_withCause(t *testing.T) {
	err := Wrap(os.ErrNotExist, http.StatusBadGateway, "config.Read", "unable to read %s", "app.env")

	assert.Equal(t,
		"infrapi-error: status=502 | title=Bad Gateway | origin=config.Read"+
			" | detail=unable to read app.env | cause=file does not exist",
		err.Error())
}

// Wrap without a format takes the message of the cause, which must not then be
// repeated as a cause= field.
func TestError_causeIsDetail(t *testing.T) {
	err := Wrap(os.ErrNotExist, http.StatusBadGateway, "config.Read", "")

	assert.Equal(t,
		"infrapi-error: status=502 | title=Bad Gateway | origin=config.Read | detail=file does not exist",
		err.Error())
}

func TestError_unwrap(t *testing.T) {
	err := Wrap(fmt.Errorf("open app.env: %w", os.ErrNotExist), http.StatusBadGateway, "config.Read", "")

	assert.True(t, stderrors.Is(err, os.ErrNotExist), "the wrapped cause must stay reachable")
	assert.Nil(t, New(http.StatusNotFound, "user.Get", "nothing here").Unwrap())
}

func TestError_payload(t *testing.T) {
	err := New(http.StatusConflict, "subscription.Create", "id %q is taken", "sub-1").
		WithType("https://infrapi.example.com/errors/duplicate", "Duplicate subscription").
		WithSolution("pick another id")
	err.Instance = "/subscriptions/sub-1"

	payload, jsonErr := json.Marshal(err)
	require.NoError(t, jsonErr)

	assert.JSONEq(t, `{
		"type": "https://infrapi.example.com/errors/duplicate",
		"status": 409,
		"title": "Duplicate subscription",
		"detail": "id \"sub-1\" is taken",
		"instance": "/subscriptions/sub-1",
		"origin": "subscription.Create",
		"solution": "pick another id"
	}`, string(payload))
}

// An untyped problem is about:blank, where the title is the status phrase, and
// the optional members are left out. The cause never reaches the payload.
func TestError_payloadUntyped(t *testing.T) {
	payload, jsonErr := json.Marshal(Wrap(os.ErrNotExist, http.StatusBadGateway, "config.Read", "upstream refused the read"))
	require.NoError(t, jsonErr)

	assert.JSONEq(t, `{
		"status": 502,
		"title": "Bad Gateway",
		"detail": "upstream refused the read",
		"origin": "config.Read"
	}`, string(payload))
}

func TestErrorCode(t *testing.T) {
	assert.Equal(t, 0, ErrorCode(nil))
	assert.Equal(t, http.StatusNotFound, ErrorCode(New(http.StatusNotFound, "user.Get", "nothing here")))

	// an Error found deeper in the chain still gives its code
	wrapped := fmt.Errorf("handler: %w", New(http.StatusTeapot, "pot.Brew", "short and stout"))
	assert.Equal(t, http.StatusTeapot, ErrorCode(wrapped))

	// an error nobody identified is a server fault
	assert.Equal(t, http.StatusInternalServerError, ErrorCode(fmt.Errorf("hello world")))
}

func TestErrorCast(t *testing.T) {
	assert.Nil(t, ErrorCast(nil))

	own := New(http.StatusNotFound, "user.Get", "no user with id %d", 42)
	assert.Same(t, own, ErrorCast(fmt.Errorf("handler: %w", own)))

	foreign := fmt.Errorf("open app.env: %w", os.ErrNotExist)
	cast := ErrorCast(foreign)
	require.NotNil(t, cast)
	assert.Equal(t, http.StatusInternalServerError, cast.Code)
	assert.Equal(t, foreign.Error(), cast.Message)
	assert.True(t, stderrors.Is(cast, os.ErrNotExist), "casting must not drop the chain")

	payload, jsonErr := json.Marshal(cast)
	require.NoError(t, jsonErr)
	assert.JSONEq(t, `{
		"status": 500,
		"title": "Internal Server Error",
		"detail": "open app.env: file does not exist"
	}`, string(payload))
}
