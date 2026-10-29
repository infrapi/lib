package vault

import (
	"errors"
	"fmt"
	"net/http"

	api "github.com/hashicorp/vault-client-go"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// classify turns a failed call to Vault into the [infraerrors.Error] it
// stands for, the call's own error as its cause:
//
//	404                     NotFound
//	401, 403                UpstreamAuth: Vault refused the client's token
//	5xx                     Upstream, retryable
//	another status          Upstream
//	no answer at all        Unavailable
func classify(err error, format string, args ...any) error {
	what := "vault: " + fmt.Sprintf(format, args...)
	var e *infraerrors.Error
	var re *api.ResponseError
	switch {
	case !errors.As(err, &re):
		e = infraerrors.Unavailable("%s: %v", what, err)
	case re.StatusCode == http.StatusNotFound:
		e = infraerrors.NotFound("%s: not found", what)
	case re.StatusCode == http.StatusUnauthorized || re.StatusCode == http.StatusForbidden:
		e = infraerrors.UpstreamAuth("%s: refused: status %d", what, re.StatusCode)
	default:
		e = infraerrors.Upstream(re.StatusCode >= http.StatusInternalServerError, "%s: status %d", what, re.StatusCode)
	}
	e.Err = err
	return e
}
