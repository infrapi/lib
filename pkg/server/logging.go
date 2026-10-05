package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// LogFormatJSON is the INFRAPI_APP_LOG_FORMAT value that makes the engine log
// one JSON object per line. Anything else keeps gin's own text lines.
const LogFormatJSON = "json"

// accessEntry is one access log line in JSON. time, level and msg are the
// names logrus's JSONFormatter uses, so a service logging through logrus with
// that formatter writes lines a collector parses the same way.
type accessEntry struct {
	Time      string  `json:"time"`
	Level     string  `json:"level"`
	Msg       string  `json:"msg"`
	Method    string  `json:"method"`
	Path      string  `json:"path"`
	Route     string  `json:"route"`
	Status    int     `json:"status"`
	LatencyMs float64 `json:"latency_ms"`
	ClientIP  string  `json:"client_ip"`
	Size      int     `json:"size"`
	UserAgent string  `json:"user_agent,omitempty"`
	RequestID string  `json:"request_id,omitempty"`
	Errors    string  `json:"errors,omitempty"`
}

// panicEntry is the line a recovered panic leaves, its stack in one field
// rather than spread over lines a collector would read as separate entries.
type panicEntry struct {
	Time   string `json:"time"`
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Panic  string `json:"panic"`
	Stack  string `json:"stack"`
}

// requestIDHeader is read from the request, then from the response, so the
// line carries the id a gateway or the service itself attached.
const requestIDHeader = "X-Request-Id"

// logMiddlewares returns the access log and the panic recovery for format,
// writing to out. Recovery comes second so that a panicking request is still
// logged, with the 500 it was answered with.
func logMiddlewares(format string, out io.Writer) []gin.HandlerFunc {
	if format != LogFormatJSON {
		return []gin.HandlerFunc{gin.LoggerWithWriter(out), gin.RecoveryWithWriter(out)}
	}
	return []gin.HandlerFunc{jsonAccessLog(out), jsonRecovery(out)}
}

// logFieldsKey is where the access log keeps the fields a request adds.
type logFieldsKey struct{}

// logFields are the fields a service attached to one request. Guarded, since a
// handler may hand its context to goroutines of its own.
type logFields struct {
	mu     sync.Mutex
	values map[string]any
}

// reservedFields are the members the engine writes itself. A service field of
// the same name is dropped rather than allowed to hide what the engine saw.
var reservedFields = map[string]bool{
	"time": true, "level": true, "msg": true, "method": true, "path": true, "route": true,
	"status": true, "latency_ms": true, "client_ip": true, "size": true, "user_agent": true,
	"request_id": true, "errors": true, "panic": true, "stack": true,
}

// LogField adds key to the JSON access log line of the request ctx belongs to:
// a tenant, a resource id, an outcome — whatever the service knows and the
// engine cannot. Call it from a gin handler with the *gin.Context itself, or
// from a net/http handler mounted with gin.WrapH with the request's context.
// The value must marshal to JSON; adding the same key again replaces it.
//
// It does nothing when the request is not logged as JSON, and drops the names
// the engine writes itself (time, level, msg, method, path, route, status,
// latency_ms, client_ip, size, user_agent, request_id, errors).
func LogField(ctx context.Context, key string, value any) {
	if gc, ok := ctx.(*gin.Context); ok {
		ctx = gc.Request.Context()
	}
	f, ok := ctx.Value(logFieldsKey{}).(*logFields)
	if !ok || key == "" || reservedFields[key] {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.values == nil {
		f.values = map[string]any{}
	}
	f.values[key] = value
}

// writeLine writes v as one line in a single Write, so concurrent requests
// never interleave inside a line. extra is appended after v's own members, in
// key order; a value that does not marshal is written as its %v text.
func writeLine(out io.Writer, v any, extra map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if len(extra) > 0 {
		keys := make([]string, 0, len(extra))
		for k := range extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b = b[:len(b)-1] // the closing brace
		for _, k := range keys {
			name, _ := json.Marshal(k)
			value, err := json.Marshal(extra[k])
			if err != nil {
				value, _ = json.Marshal(fmt.Sprint(extra[k]))
			}
			b = append(append(append(append(b, ','), name...), ':'), value...)
		}
		b = append(b, '}')
	}
	_, _ = out.Write(append(b, '\n'))
}

func jsonAccessLog(out io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		fields := &logFields{}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), logFieldsKey{}, fields))

		c.Next()

		status := c.Writer.Status()
		level := "info"
		switch {
		case status >= http.StatusInternalServerError:
			level = "error"
		case status >= http.StatusBadRequest:
			level = "warning"
		}

		requestID := c.Request.Header.Get(requestIDHeader)
		if requestID == "" {
			requestID = c.Writer.Header().Get(requestIDHeader)
		}

		size := c.Writer.Size()
		if size < 0 {
			size = 0
		}

		writeLine(out, accessEntry{
			Time:      start.Format(time.RFC3339Nano),
			Level:     level,
			Msg:       "request",
			Method:    c.Request.Method,
			Path:      path,
			Route:     c.FullPath(),
			Status:    status,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000,
			ClientIP:  c.ClientIP(),
			Size:      size,
			UserAgent: c.Request.UserAgent(),
			RequestID: requestID,
			Errors:    c.Errors.ByType(gin.ErrorTypePrivate).String(),
		}, fields.snapshot())
	}
}

func jsonRecovery(out io.Writer) gin.HandlerFunc {
	// gin still tells a broken connection from a panic, and does not call the
	// handler for the former: there is nobody left to answer.
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		writeLine(out, panicEntry{
			Time:   time.Now().Format(time.RFC3339Nano),
			Level:  "error",
			Msg:    "panic recovered",
			Method: c.Request.Method,
			Path:   c.Request.URL.Path,
			Panic:  fmt.Sprint(recovered),
			Stack:  string(debug.Stack()),
		}, nil)
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

func (f *logFields) snapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]any, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out
}
