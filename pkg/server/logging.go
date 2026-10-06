package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
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

// brokenEntry is the line a connection the client closed leaves.
type brokenEntry struct {
	Time   string `json:"time"`
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Error  string `json:"error"`
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
	"request_id": true, "errors": true, "panic": true, "stack": true, "error": true,
}

// LogField adds key to the JSON access log line of the request ctx belongs to:
// a tenant, a resource id, an outcome — whatever the service knows and the
// engine cannot. Call it from a gin handler with the *gin.Context itself, or
// from a net/http handler mounted with gin.WrapH with the request's context.
// The value must marshal to JSON; adding the same key again replaces it.
//
// It does nothing when the request is not logged as JSON, and drops the names
// the engine writes itself (time, level, msg, method, path, route, status,
// latency_ms, client_ip, size, user_agent, request_id, errors, error, panic, stack).
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
		// The log is where a failure would go; the process's own log is
		// what is left when that is the failure.
		log.Printf("server: log line dropped: %v", err)
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
			name, err := json.Marshal(k)
			if err != nil {
				log.Printf("server: log field %q dropped: %v", k, err)
				continue
			}
			value, err := json.Marshal(extra[k])
			if err != nil {
				if value, err = json.Marshal(fmt.Sprint(extra[k])); err != nil {
					log.Printf("server: log field %q dropped: %v", k, err)
					continue
				}
			}
			b = append(append(append(append(b, ','), name...), ':'), value...)
		}
		b = append(b, '}')
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		log.Printf("server: log line lost: %v", err)
	}
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

// jsonRecovery is gin's recovery with its lines in JSON: the same broken
// connection test, the same 500, the same stack. Only the request dump gin
// adds in debug mode is left out — it prints every header but Authorization,
// an apikey header included.
func jsonRecovery(out io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// A client that went away is not a bug: nobody is left to answer,
			// and a stack would only be noise.
			if err, ok := recovered.(error); ok && brokenConnection(err) {
				writeLine(out, brokenEntry{
					Time:   time.Now().Format(time.RFC3339Nano),
					Level:  "warning",
					Msg:    "client connection broken",
					Method: c.Request.Method,
					Path:   c.Request.URL.Path,
					Error:  err.Error(),
				}, nil)
				// On the request's errors, for its access line.
				c.Errors = append(c.Errors, &gin.Error{Err: err, Type: gin.ErrorTypePrivate})
				c.Abort()
				return
			}

			writeLine(out, panicEntry{
				Time:   time.Now().Format(time.RFC3339Nano),
				Level:  "error",
				Msg:    "panic recovered",
				Method: c.Request.Method,
				Path:   c.Request.URL.Path,
				Panic:  fmt.Sprint(recovered),
				Stack:  panicStack(stackSkip),
			}, nil)
			c.AbortWithStatus(http.StatusInternalServerError)
		}()
		c.Next()
	}
}

// brokenConnection is gin's test for a connection the client closed.
func brokenConnection(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, http.ErrAbortHandler)
}

// stackSkip starts the stack where the panic was raised: past panicStack
// itself, the deferred function calling it, and the runtime's panic.
const stackSkip = 3

// panicStack is the stack in gin's own text format, one frame per pair of
// lines:
//
//	/src/handler.go:42 (0x4f2a1c)
//		(*Handler).Get: return h.store.Get(id)
//
// The source line is read from the file when the file is there; when it is
// not, as in an image that ships only the binary, it is "???". gin skips the
// function line entirely in that case, which is where it would matter most.
func panicStack(skip int) string {
	var (
		buf      strings.Builder
		lastFile string
		lines    []string
	)
	for i := skip; ; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		fmt.Fprintf(&buf, "%s:%d (0x%x)\n", file, line, pc)
		if file != lastFile {
			lines = sourceLines(file)
			lastFile = file
		}
		src := "???"
		if line > 0 && line <= len(lines) {
			src = strings.TrimSpace(lines[line-1])
		}
		fmt.Fprintf(&buf, "\t%s: %s\n", functionName(pc), src)
	}
	return buf.String()
}

// sourceLines reads a source file for the stack, or nothing when it cannot.
func sourceLines(file string) []string {
	// The path comes from the binary's own debug information, never from a
	// request, and the file is only read to quote one line of it.
	b, err := os.ReadFile(file) // #nosec G304 -- path from runtime.Caller, not from input
	if err != nil {
		return nil
	}
	return strings.Split(string(b), "\n")
}

// functionName is gin's: the name without its package path, so that
// github.com/x/y.(*T).M reads (*T).M — the file already says where it is.
func functionName(pc uintptr) string {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "???"
	}
	name := fn.Name()
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	if period := strings.IndexByte(name, '.'); period >= 0 {
		name = name[period+1:]
	}
	return strings.ReplaceAll(name, "·", ".")
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
