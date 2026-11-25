package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/infrapi/lib/pkg/useragent"
)

type headersKey struct{}

// withHeaders makes the requests to the bridged server made with ctx carry
// h: each call brings its own credential, over a session shared by every
// caller.
func withHeaders(ctx context.Context, h http.Header) context.Context {
	return context.WithValue(ctx, headersKey{}, h)
}

// headerTransport adds the static headers, then the call's own.
type headerTransport struct {
	static http.Header
	next   http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, vs := range t.static {
		req.Header[k] = vs
	}
	if h, ok := req.Context().Value(headersKey{}).(http.Header); ok {
		for k, vs := range h {
			req.Header[k] = vs
		}
	}
	return t.next.RoundTrip(req)
}

// upstream keeps one MCP session to the bridged server, opened again when
// it breaks.
type upstream struct {
	url      string
	protocol string
	client   *sdk.Client
	http     *http.Client

	mu      sync.Mutex
	session *sdk.ClientSession
}

func newUpstream(cfg Config) *upstream {
	base := cfg.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &upstream{
		url:      cfg.URL,
		protocol: cfg.ProtocolVersion,
		client:   sdk.NewClient(&sdk.Implementation{Name: "infrapi-bridge", Version: "v1"}, nil),
		// the bridged server's logs name the service (useragent)
		http: &http.Client{Transport: headerTransport{static: cfg.Headers, next: useragent.Transport(base)}, Timeout: cfg.Timeout},
	}
}

func (u *upstream) connect(ctx context.Context) (*sdk.ClientSession, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.session != nil {
		return u.session, nil
	}
	// The session outlives the call that opens it: it keeps this call's
	// headers for its initialize request, not its cancellation.
	h, _ := ctx.Value(headersKey{}).(http.Header)
	s, err := u.client.Connect(withHeaders(context.Background(), h), &sdk.StreamableClientTransport{
		Endpoint:             u.url,
		HTTPClient:           u.http,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, &sdk.ClientSessionOptions{ProtocolVersion: u.protocol})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", u.url, err)
	}
	u.session = s
	return s, nil
}

// drop forgets s, if it is still the session, so that the next call opens
// another. Closing a broken session may fail: it is gone either way.
func (u *upstream) drop(s *sdk.ClientSession) error {
	u.mu.Lock()
	if u.session != s {
		u.mu.Unlock()
		return nil
	}
	u.session = nil
	u.mu.Unlock()
	return s.Close()
}

// do runs fn over the session, with headers, once again on a new session
// when the call fails below the tool (the server restarted, the session
// expired). A tool's own failure is a result, never retried.
func (u *upstream) do(parent context.Context, headers http.Header, fn func(context.Context, *sdk.ClientSession) error) error {
	// The request to the service carries values of its own (the MCP
	// protocol version its client negotiated, for one) that must not reach
	// the bridged server: its cancellation only.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(parent, cancel)
	defer stop()
	ctx = withHeaders(ctx, headers)

	var errs []error
	for attempt := 0; attempt < 2; attempt++ {
		s, err := u.connect(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		err = fn(ctx, s)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
		if cerr := u.drop(s); cerr != nil {
			errs = append(errs, fmt.Errorf("close the broken session: %w", cerr))
		}
	}
	return errors.Join(errs...)
}

func (u *upstream) listTools(ctx context.Context) ([]*sdk.Tool, error) {
	var tools []*sdk.Tool
	err := u.do(ctx, nil, func(ctx context.Context, s *sdk.ClientSession) error {
		tools = nil
		for t, err := range s.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			tools = append(tools, t)
		}
		return nil
	})
	return tools, err
}

func (u *upstream) callTool(ctx context.Context, headers http.Header, name string, args map[string]any) (*sdk.CallToolResult, error) {
	var res *sdk.CallToolResult
	err := u.do(ctx, headers, func(ctx context.Context, s *sdk.ClientSession) error {
		var err error
		res, err = s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		return err
	})
	return res, err
}

// close ends the session, if one is open.
func (u *upstream) close() error {
	u.mu.Lock()
	s := u.session
	u.session = nil
	u.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}
