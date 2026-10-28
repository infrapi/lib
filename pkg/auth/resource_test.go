package auth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseResource(t *testing.T) {
	cases := []struct {
		raw          string
		wantResource string
		wantHasID    bool
	}{
		{"/vm", "vm", false},
		{"vm", "vm", false},
		{"/vm/123", "vm", true},
		{"/vm/123/reboot", "vm/reboot", false},
		{"/vm/123/reboot/456", "vm/reboot", true},
	}

	for _, tc := range cases {
		resource, hasID, err := ParseResource(tc.raw)
		require.NoError(t, err, tc.raw)
		assert.Equal(t, tc.wantResource, resource, tc.raw)
		assert.Equal(t, tc.wantHasID, hasID, tc.raw)
	}
}

func TestParseResource_rejectsEmpty(t *testing.T) {
	_, _, err := ParseResource("/")
	assert.Error(t, err)

	_, _, err = ParseResource("")
	assert.Error(t, err)
}

func TestVerbFor(t *testing.T) {
	cases := []struct {
		method string
		hasID  bool
		want   string
	}{
		{http.MethodGet, false, "list"},
		{http.MethodGet, true, "get"},
		{http.MethodPost, false, "create"},
		{http.MethodPut, true, "update"},
		{http.MethodPatch, false, "patch"},
		{http.MethodPatch, true, "patch"},
		{http.MethodDelete, true, "delete"},
	}

	for _, tc := range cases {
		got, err := VerbFor(tc.method, tc.hasID)
		require.NoError(t, err, tc.method)
		assert.Equal(t, tc.want, got, tc.method)
	}
}

func TestVerbFor_rejectsUnknownMethod(t *testing.T) {
	_, err := VerbFor(http.MethodOptions, false)
	assert.Error(t, err)
}
