package events

import "github.com/gin-gonic/gin"

// requestKey keeps, in a request's gin context, what its handler says of
// the event server.WithEvents records for it.
const requestKey = "events.request"

// Request is what a handler said of its request's event.
type Request struct {
	Details map[string]any
	Note    string
	Reason  string
	Skip    bool
}

func request(c *gin.Context) *Request {
	if v, ok := c.Get(requestKey); ok {
		if r, ok := v.(*Request); ok {
			return r
		}
	}
	r := &Request{}
	c.Set(requestKey, r)
	return r
}

// Annotate adds a detail to the request's event.
func Annotate(c *gin.Context, key string, value any) {
	r := request(c)
	if r.Details == nil {
		r.Details = map[string]any{}
	}
	r.Details[key] = value
}

// SetNote is the request's event's note: what a human reads.
func SetNote(c *gin.Context, note string) { request(c).Note = note }

// SetReason replaces the request's event's reason.
func SetReason(c *gin.Context, reason string) { request(c).Reason = reason }

// Skip records no event for the request.
func Skip(c *gin.Context) { request(c).Skip = true }

// FromRequest is what the handler said of the request's event.
func FromRequest(c *gin.Context) Request { return *request(c) }
