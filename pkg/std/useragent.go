// Package std holds the small helpers, standard library only, that lib's
// packages and the services share.
package std

// The User-Agent of the HTTP requests infrapi/lib makes for a service: to
// Kayra (auth, events), to a bridged MCP server, to another API (client).
// The service's module and version, from the binary's build information,
// as one product token (RFC 9110):
//
//	tofu-api/v0.13.0
//
// so that the logs of whoever is called tell which service, at which
// version, called; Go's own default, Go-http-client/1.1, tells nothing.

import (
	"net/http"
	"path"
	"regexp"
	"runtime/debug"
)

var userAgent = build(debug.ReadBuildInfo)

// UserAgent is the User-Agent: <service>/<version>.
func UserAgent() string { return userAgent }

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

// UserAgentTransport sets the User-Agent of every request that has none, then
// passes it to next (http.DefaultTransport when nil). The request is
// cloned, never changed: a RoundTripper must not modify it.
func UserAgentTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return userAgentTransport{next: next}
}

type userAgentTransport struct{ next http.RoundTripper }

func (t userAgentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") != "" {
		return t.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", userAgent)
	return t.next.RoundTrip(r)
}

// UserAgentClient is c with UserAgentTransport around its transport, a copy: c itself is not
// changed. A new client with only UserAgentTransport when c is nil.
func UserAgentClient(c *http.Client) *http.Client {
	if c == nil {
		return &http.Client{Transport: UserAgentTransport(nil)}
	}
	out := *c
	out.Transport = UserAgentTransport(c.Transport)
	return &out
}
