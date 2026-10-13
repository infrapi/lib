package errors

import (
	"fmt"
	"net/http"
)

// newf builds an Error with a stable Slug, a title that describes the
// problem type rather than the occurrence, and an explicit Retryable —
// unlike New, which leaves Slug and Retryable unset for a one-off error
// nobody else needs to branch on.
func newf(code int, slug, title string, retryable bool, format string, args ...any) *Error {
	e := &Error{Code: code, Slug: slug, Title: title, Message: fmt.Sprintf(format, args...)}
	return e.WithRetryable(retryable)
}

// Invalid is a request that can never succeed as it is.
func Invalid(format string, args ...any) *Error {
	return newf(http.StatusBadRequest, "invalid_request", "Invalid request", false, format, args...)
}

// NotFound is a resource that does not exist.
func NotFound(format string, args ...any) *Error {
	return newf(http.StatusNotFound, "not_found", "Not found", false, format, args...)
}

// AlreadyExists carries what is there, so the caller can tell its own earlier
// attempt from somebody else's object.
func AlreadyExists(current any, format string, args ...any) *Error {
	return newf(http.StatusConflict, "already_exists", "Already exists", false, format, args...).WithCurrent(current)
}

// NotEmpty refuses a delete that would take other things with it.
func NotEmpty(format string, args ...any) *Error {
	return newf(http.StatusConflict, "folder_not_empty", "Not empty", false, format, args...)
}

// Protected refuses to change what something other than the caller owns —
// a tenant's own team, which onboarding created and every private resource
// depends on.
func Protected(format string, args ...any) *Error {
	return newf(http.StatusConflict, "protected", "Protected", false, format, args...)
}

// NotOnboarded is a tenant whose prerequisites do not exist on the platform.
func NotOnboarded(format string, args ...any) *Error {
	return newf(http.StatusUnprocessableEntity, "tenant_not_onboarded", "Tenant not onboarded", false, format, args...)
}

// TooLarge is a body over the size the API accepts.
func TooLarge(format string, args ...any) *Error {
	return newf(http.StatusRequestEntityTooLarge, "too_large", "Request too large", false, format, args...)
}

// UpstreamAuth is the system behind the API refusing the service's own
// credentials. Not the caller's fault, and not something a retry fixes: the
// configuration does.
func UpstreamAuth(format string, args ...any) *Error {
	return newf(http.StatusBadGateway, "upstream_auth", "Upstream refused our credentials", false, format, args...)
}

// Upstream is the system behind the API failing; retryable says whether that
// failure is the kind that passes.
func Upstream(retryable bool, format string, args ...any) *Error {
	return newf(http.StatusBadGateway, "upstream_error", "Upstream error", retryable, format, args...)
}

// Unavailable is the system behind the API not answering at all.
func Unavailable(format string, args ...any) *Error {
	return newf(http.StatusServiceUnavailable, "upstream_unavailable", "Upstream unavailable", true, format, args...)
}
