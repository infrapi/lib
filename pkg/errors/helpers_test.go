package errors

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refusal says what it is and whether retrying helps.
func TestHelpers_retryability(t *testing.T) {
	err := Invalid("bad field %q", "name")
	require.NotNil(t, err.Retryable)
	assert.False(t, *err.Retryable)
	assert.Equal(t, http.StatusBadRequest, err.Code)
	assert.Equal(t, "invalid_request", err.Slug)

	err = Unavailable("no route to host")
	require.NotNil(t, err.Retryable)
	assert.True(t, *err.Retryable)
}

// AlreadyExists carries what is there, so the caller can tell its own
// earlier attempt from somebody else's object.
func TestAlreadyExists_current(t *testing.T) {
	err := AlreadyExists(map[string]string{"id": "a"}, "taken")

	assert.Equal(t, http.StatusConflict, err.Code)
	require.NotNil(t, err.Retryable)
	assert.False(t, *err.Retryable)
	assert.Equal(t, map[string]string{"id": "a"}, err.Current)

	payload, err2 := json.Marshal(err)
	require.NoError(t, err2)
	assert.JSONEq(t, `{
		"status": 409,
		"title": "Already exists",
		"detail": "taken",
		"code": "already_exists",
		"retryable": false,
		"current": {"id": "a"}
	}`, string(payload))
}

// A bare New or Wrap leaves Slug and Retryable unset, so neither "code" nor
// "retryable" appears: a caller must not confuse a one-off internal error
// with the common, branchable shapes the helpers above build.
func TestNew_leavesSlugAndRetryableUnset(t *testing.T) {
	err := New(http.StatusBadGateway, "config.Read", "upstream refused the read")

	assert.Empty(t, err.Slug)
	assert.Nil(t, err.Retryable)

	payload, jsonErr := json.Marshal(err)
	require.NoError(t, jsonErr)
	assert.JSONEq(t, `{
		"status": 502,
		"title": "Bad Gateway",
		"detail": "upstream refused the read",
		"origin": "config.Read"
	}`, string(payload))
}
