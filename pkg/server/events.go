package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/api"
	infraerrors "github.com/infrapi/lib/pkg/errors"
	"github.com/infrapi/lib/pkg/events"
)

// CheckedUserKey is where pkg/auth's RequireAccess keeps the caller's
// subject, as Kayra's check answered it: a request that has it passed the
// check.
const CheckedUserKey = "auth.checked_user"

// EventHooks adjust the event WithEvents records for a request: Before
// says whether to record one at all, After changes it before it goes.
type EventHooks struct {
	Before func(c *gin.Context) bool
	After  func(c *gin.Context, e *events.Event)
}

// WithEvents records an event for every write the service serves (create,
// update, patch, delete, an action) once its handler answered, with rec:
//
//   - 2xx: Normal, its reason the contract's operation in the past
//     (createVM: VMCreated), or x-infrapi-event's reason;
//   - 202: Normal, VMRebootAccepted: the worker reports how it ended;
//   - a 4xx from the handler, a 5xx: Warning, VMCreateFailed, its note
//     the error's detail.
//
// A request that did not pass Kayra's check (pkg/auth's RequireAccess), or
// that its contract refused, gets none: the audit has it. An operation with
// x-infrapi-event: false gets none either, nor a request whose handler
// called events.Skip.
func WithEvents(rec *events.Recorder, hooks ...EventHooks) Option {
	return func(o *options) error {
		o.events = rec
		o.eventHooks = hooks
		return nil
	}
}

func eventsMiddleware(rec *events.Recorder, hooks []EventHooks) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}
		for _, h := range hooks {
			if h.Before != nil && !h.Before(c) {
				c.Next()
				return
			}
		}
		// a create names its object in its body, not its path: its first
		// bytes read now, then handed to the handler with the rest
		var head []byte
		if c.Request.Body != nil {
			head, _ = io.ReadAll(io.LimitReader(c.Request.Body, maxNamedBody))
			c.Request.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(head), c.Request.Body), c.Request.Body}
		}
		c.Next()

		user := c.GetString(CheckedUserKey)
		said := events.FromRequest(c)
		if user == "" || said.Skip {
			return
		}
		op, _ := c.Get(operationKey)
		operation, _ := op.(*openapi3.Operation)
		custom, off := eventExtension(operation)
		if off {
			return
		}
		p, err := api.Parse(c.Request.URL.Path)
		if err != nil {
			return
		}
		kind, verb := operationParts(operation, p.Resource, method)
		name := nameOf(p.Resource)
		if name == "" {
			var named struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(head, &named) == nil {
				name = named.Name
			}
		}
		e := events.Event{
			Regarding: events.Regarding{API: p.API, Namespace: p.Namespace, Resource: resourceOf(p.Resource), Name: name},
			Action:    verb, User: user, RequestID: c.GetHeader(RequestIDHeader), Details: said.Details, Note: said.Note,
		}
		status := c.Writer.Status()
		switch {
		case status == http.StatusAccepted:
			e.Type, e.Reason = events.Normal, kind+upper(verb)+"Accepted"
		case status < 400:
			e.Type, e.Reason = events.Normal, kind+past(verb)
			if custom != "" {
				e.Reason = custom
			}
		default:
			e.Type, e.Reason = events.Warning, kind+upper(verb)+"Failed"
			if e.Note == "" {
				e.Note = failure(c, status)
			}
		}
		if said.Reason != "" {
			e.Reason = said.Reason
		}
		for _, h := range hooks {
			if h.After != nil {
				h.After(c, &e)
			}
		}
		rec.Emit(e)
	}
}

// eventExtension reads an operation's x-infrapi-event: false turns its
// event off, {"reason": "..."} names its success's reason.
func eventExtension(op *openapi3.Operation) (reason string, off bool) {
	if op == nil {
		return "", false
	}
	switch v := op.Extensions["x-infrapi-event"].(type) {
	case bool:
		return "", !v
	case map[string]any:
		r, _ := v["reason"].(string)
		return r, false
	}
	return "", false
}

// operationParts are an operation's kind and verb, from its operationId
// (createVM: VM, create); without a contract, from the path and method
// (vms/web-1, POST: Vm, create).
func operationParts(op *openapi3.Operation, resource, method string) (kind, verb string) {
	if op != nil && op.OperationID != "" {
		id := op.OperationID
		i := strings.IndexFunc(id, unicode.IsUpper)
		if i > 0 {
			return id[i:], id[:i]
		}
	}
	segs := strings.Split(strings.Trim(resource, "/"), "/")
	kind = upper(strings.TrimSuffix(segs[0], "s"))
	switch method {
	case http.MethodPost:
		verb = "create"
		if len(segs)%2 == 1 && len(segs) > 1 {
			verb = segs[len(segs)-1] // an action
		}
	case http.MethodPut:
		verb = "update"
	case http.MethodPatch:
		verb = "patch"
	case http.MethodDelete:
		verb = "delete"
	}
	return kind, verb
}

// past is a verb's past, capitalized: Created, Replaced, Rebooted.
func past(verb string) string {
	switch verb {
	case "":
		return "Done"
	case "list", "get":
		return upper(verb) + "Done"
	}
	v := upper(verb)
	switch {
	case strings.HasSuffix(v, "e"):
		return v + "d"
	case strings.HasSuffix(v, "y") && len(v) > 1 && !strings.ContainsRune("aeiou", rune(v[len(v)-2])):
		return v[:len(v)-1] + "ied"
	}
	return v + "ed"
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// resourceOf and nameOf take a resource path apart: vms/web-1/reboot is
// vms, web-1.
func resourceOf(resource string) string {
	segs := strings.Split(strings.Trim(resource, "/"), "/")
	return segs[0]
}

func nameOf(resource string) string {
	segs := strings.Split(strings.Trim(resource, "/"), "/")
	if len(segs) < 2 {
		return ""
	}
	return segs[1]
}

// failure is a failed request's note: its error's detail.
func failure(c *gin.Context, status int) string {
	if err := ErrorOf(c); err != nil {
		var e *infraerrors.Error
		if infraerrors.As(err, &e) && e.Message != "" {
			return e.Message
		}
		if status < 500 {
			return err.Error()
		}
	}
	return http.StatusText(status)
}

// maxNamedBody is how much of a request's body is kept for its object's
// name.
const maxNamedBody = 64 << 10
