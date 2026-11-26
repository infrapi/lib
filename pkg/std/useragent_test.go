package std

import (
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func info(main, mainVersion string) func() (*debug.BuildInfo, bool) {
	return binary("", main, mainVersion)
}

func binary(pkg, main, mainVersion string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Path: pkg, Main: debug.Module{Path: main, Version: mainVersion}}, true
	}
}

func TestUserAgent_build(t *testing.T) {
	for name, c := range map[string]struct {
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		"a service":        {info("github.com/infrapi/tofu-api", "v0.13.0"), "tofu-api/v0.13.0"},
		"a major version":  {info("github.com/acme/compute/v2", "v2.1.0"), "compute/v2.1.0"},
		"not tagged":       {info("github.com/infrapi/ulgen", "(devel)"), "ulgen/devel"},
		"no build info":    {func() (*debug.BuildInfo, bool) { return nil, false }, "infrapi-service"},
		"no main path":     {info("", ""), "infrapi-service/devel"},
		"its binary":       {binary("gitlab.example.com/ops/app/cmd/puppet", "gitlab.example.com/ops/app", "v0.4.0"), "puppet/v0.4.0"},
		"main at the root": {binary("github.com/infrapi/tofu-api", "github.com/infrapi/tofu-api", "v0.13.0"), "tofu-api/v0.13.0"},
		"a test binary":    {binary("github.com/infrapi/ulgen/internal/kayra.test", "github.com/infrapi/ulgen", "(devel)"), "ulgen/devel"},
	} {
		assert.Equal(t, c.want, build(c.read), name)
	}
}

// UserAgentTransport sets the User-Agent of a request without one, keeps one set,
// and leaves the caller's request as it was; UserAgentClient wraps a copy.
func TestUserAgentTransport(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.UserAgent())
	}))
	defer srv.Close()

	base := &http.Client{}
	c := UserAgentClient(base)
	assert.Nil(t, base.Transport, "the caller's client is not changed")

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Del("User-Agent")
	resp, err := c.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Empty(t, req.Header.Get("User-Agent"), "the request is not changed")

	own, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	own.Header.Set("User-Agent", "mine/1")
	resp, err = UserAgentClient(nil).Do(own)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, []string{UserAgent(), "mine/1"}, got)
	assert.NotEmpty(t, UserAgent())
}
