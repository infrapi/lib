// Package useragent names the service in the User-Agent of the HTTP requests
// infrapi/lib makes for it: to Kayra (auth, events), to a bridged MCP server,
// to another API (client). The service's module and version, from the
// binary's build information, as one product token (RFC 9110):
//
//	tofu-api/v0.13.0
//
// so that the logs of whoever is called tell which service, at which
// version, called; Go's own default, Go-http-client/1.1, tells nothing.
package useragent

import (
	"net/http"
	"path"
	"regexp"
	"runtime/debug"
)

var value = build(debug.ReadBuildInfo)

// String is the User-Agent: <service>/<version>.
func String() string { return value }

var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)

// build reads the main module's last path element and version.
func build(read func() (*debug.BuildInfo, bool)) string {
	info, ok := read()
	if !ok || info == nil {
		return "infrapi-service"
	}
	name := path.Base(info.Main.Path)
	if majorSuffix.MatchString(name) { // github.com/acme/compute/v2
		name = path.Base(path.Dir(info.Main.Path))
	}
	if name == "." || name == "/" || name == "" {
		name = "infrapi-service"
	}
	return name + "/" + version(info.Main.Version)
}

func version(v string) string {
	if v == "" || v == "(devel)" {
		return "devel"
	}
	return v
}

// Transport sets the User-Agent of every request that has none, then
// passes it to next (http.DefaultTransport when nil). The request is
// cloned, never changed: a RoundTripper must not modify it.
func Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return transport{next: next}
}

type transport struct{ next http.RoundTripper }

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") != "" {
		return t.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", value)
	return t.next.RoundTrip(r)
}

// Client is c with Transport around its transport, a copy: c itself is not
// changed. A new client with only Transport when c is nil.
func Client(c *http.Client) *http.Client {
	if c == nil {
		return &http.Client{Transport: Transport(nil)}
	}
	out := *c
	out.Transport = Transport(c.Transport)
	return &out
}
