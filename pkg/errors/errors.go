// Package errors carries the application error InfraPI services return. It
// serialises as an RFC 9457 problem detail, so serve it as MediaType.
//
// Build one from scratch when the service itself refuses the request. The
// second argument says where it broke, and travels as the "origin" extension
// member:
//
//	return errors.New(http.StatusNotImplemented, "subscription.SetState", "state %q not supported", state)
//
// Wrap the cause when the failure comes from below, so errors.Is and errors.As
// keep working for the caller:
//
//	return errors.Wrap(err, http.StatusBadGateway, "subscription.Fetch", "")
//
// Give recurring problems a type URI, and a title that describes the type
// rather than the occurrence:
//
//	return errors.New(http.StatusConflict, "subscription.Create", "id %q is taken", id).
//		WithType("https://infrapi.example.com/errors/duplicate-subscription", "Duplicate subscription")
//
// Codes are the net/http constants; this package does not redefine them.
package errors

import (
	"encoding/json"
	errs "errors"
	"fmt"
	"net/http"
	"strings"
)

// MediaType is the content type of the serialised payload, per RFC 9457.
const MediaType = "application/problem+json"

// Error is an application error on its way back to an API client. Its JSON form
// is an RFC 9457 problem detail: type, status, title, detail and instance are
// the members the RFC defines, origin and solution are extension members.
type Error struct {
	// URI reference identifying the problem type. Empty means "about:blank":
	// the problem has no semantics beyond the status code.
	Type string `json:"type,omitempty"`

	// HTTP status code, from net/http. The response MUST carry the same one.
	Code int `json:"status"`

	// Short summary of the problem type, the same on every occurrence. Left
	// empty it is serialised as the status phrase, which is what "about:blank"
	// asks for.
	Title string `json:"title"`

	// Human readable explanation of this occurrence, aimed at helping the
	// client fix its request.
	Message string `json:"detail"`

	// URI reference identifying this occurrence, a request URI or an opaque id.
	Instance string `json:"instance,omitempty"`

	// Where the issue happened, in the form MyPackage.MyFunc.MyCall.
	// Example: puppetmaster.ListModules.ReadFile(checkFile)
	// Extension member: it is for your operators, not for the client.
	Origin string `json:"origin,omitempty"`

	// How to fix it. If you identified an error, you know the solution: say it.
	// Extension member.
	Solution string `json:"solution,omitempty"`

	// Err is the wrapped cause. It stays out of the payload and is reachable
	// with errors.Is and errors.As.
	Err error `json:"-"`
}

// title is the summary sent to clients: the one set on the error, or the status
// phrase, which is what "about:blank" asks for.
func (e *Error) title() string {
	if e.Title != "" {
		return e.Title
	}
	return http.StatusText(e.Code)
}

// Error implements the error interface. This is the operator facing form, with
// the members the payload leaves out.
func (e *Error) Error() string {
	parts := []string{fmt.Sprintf("status=%d", e.Code), "title=" + e.title()}

	for _, member := range [][2]string{
		{"type", e.Type},
		{"origin", e.Origin},
		{"detail", e.Message},
		{"instance", e.Instance},
		{"solution", e.Solution},
	} {
		if member[1] != "" {
			parts = append(parts, member[0]+"="+member[1])
		}
	}

	// the cause is already the detail when Wrap was called without a format
	if e.Err != nil && e.Err.Error() != e.Message {
		parts = append(parts, "cause="+e.Err.Error())
	}

	return "infrapi-error: " + strings.Join(parts, " | ")
}

// MarshalJSON writes the problem detail, filling in the title an untyped
// problem must carry.
func (e *Error) MarshalJSON() ([]byte, error) {
	// a plain copy of the fields, without the methods, so json does not recurse
	type problem Error

	p := problem(*e)
	p.Title = e.title()

	return json.Marshal(p)
}

// Unwrap returns the wrapped cause, so errors.Is and errors.As reach through
// this error.
func (e *Error) Unwrap() error { return e.Err }

// WithType sets the URI identifying the problem type and the title summarising
// it, and returns the error, to be chained on New or Wrap.
func (e *Error) WithType(uri string, title string) *Error {
	e.Type = uri
	e.Title = title
	return e
}

// WithSolution sets the remediation hint and returns the error, to be chained
// on New or Wrap.
func (e *Error) WithSolution(format string, args ...any) *Error {
	e.Solution = fmt.Sprintf(format, args...)
	return e
}

// New returns an Error with the given code, origin and formatted detail.
func New(code int, origin string, format string, args ...any) *Error {
	return &Error{
		Code:    code,
		Origin:  origin,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap returns an Error carrying err as its cause. An empty format takes the
// message of err as the detail, which is what most call sites want.
func Wrap(err error, code int, origin string, format string, args ...any) *Error {
	message := err.Error()
	if format != "" {
		message = fmt.Sprintf(format, args...)
	}

	return &Error{
		Code:    code,
		Origin:  origin,
		Message: message,
		Err:     err,
	}
}

// ErrorCode returns the code of the first Error in the chain of err. It returns
// 0 when err is nil, and http.StatusInternalServerError when the chain holds no
// Error: an error nobody identified is a server fault.
func ErrorCode(err error) int {
	if err == nil {
		return 0
	}

	var e *Error
	if errs.As(err, &e) {
		return e.Code
	}

	return http.StatusInternalServerError
}

// ErrorCast returns the first Error in the chain of err, or an untyped 500
// Error wrapping err when the chain holds none.
//
// It returns nil for a nil error, so never assign the result straight to a
// variable of type error: a nil *Error in an error interface is not nil.
func ErrorCast(err error) *Error {
	if err == nil {
		return nil
	}

	var e *Error
	if errs.As(err, &e) {
		return e
	}

	return &Error{
		Code:    http.StatusInternalServerError,
		Message: err.Error(),
		Err:     err,
	}
}
