package cluster

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func check(t *testing.T, s string) error {
	t.Helper()
	c, err := ParseCheck(s)
	require.NoError(t, err, s)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.Check(ctx)
}

func TestParseCheck_refuses(t *testing.T) {
	for _, s := range []string{
		"", "ftp://host:21", "tcp://host", "tcp://:5432", "telnet://nohostport",
		"http://", "exec:", "exec:   ", "file://host/path", "file://", "!", "!ftp://x",
	} {
		_, err := ParseCheck(s)
		assert.Error(t, err, "%q", s)
	}
}

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()

	assert.NoError(t, check(t, "tcp://"+addr))
	assert.NoError(t, check(t, "telnet://"+addr), "telnet:// is tcp://")
	require.NoError(t, ln.Close())
	assert.Error(t, check(t, "tcp://"+addr), "nothing listens any more")
	assert.NoError(t, check(t, "!tcp://"+addr))
}

func TestHTTPCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusNoContent)
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	assert.NoError(t, check(t, srv.URL+"/ok"))
	assert.ErrorContains(t, check(t, srv.URL+"/down"), "503")
	assert.ErrorContains(t, check(t, srv.URL+"/moved"), "302", "a redirect is not followed")
}

func TestExecCheck(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "is-primary")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n[ \"$1\" = dc1 ] || { echo not primary >&2; exit 1; }\n"), 0o700))

	assert.NoError(t, check(t, "exec:"+script+" dc1"), "arguments are passed")
	assert.ErrorContains(t, check(t, "exec:"+script+" dc2"), "not primary", "the command's output explains the failure")
	assert.Error(t, check(t, "exec:"+filepath.Join(dir, "missing")))
}

func TestFileCheckAndNegation(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "maintenance")

	assert.Error(t, check(t, "file://"+flag))
	assert.NoError(t, check(t, "!file://"+flag), "no maintenance flag: may lead")

	require.NoError(t, os.WriteFile(flag, nil, 0o600))
	assert.NoError(t, check(t, "file://"+flag))
	assert.ErrorContains(t, check(t, "!file://"+flag), "holds")
}

// A negated check that times out does not pass: the condition is unknown.
func TestNegationOfATimeoutFails(t *testing.T) {
	slow := CheckFunc("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	assert.Error(t, notCheck{slow}.Check(ctx))
}

func TestRunChecks_namesTheFirstFailure(t *testing.T) {
	pass := CheckFunc("pass", func(context.Context) error { return nil })
	fail := CheckFunc("fail", func(context.Context) error { return assert.AnError })
	failed, err := runChecks(context.Background(), []Check{pass, fail, pass}, time.Second)
	assert.Equal(t, "fail", failed.String())
	assert.ErrorContains(t, err, "fail: ")

	failed, err = runChecks(context.Background(), []Check{pass}, time.Second)
	assert.Nil(t, failed)
	assert.NoError(t, err)
}

// The published state flips only after threshold results in a row.
func TestNextState(t *testing.T) {
	published, streakValue, streak := true, true, 2
	results := []bool{false, true, false, false, false, true, true}
	var flips []bool
	for _, r := range results {
		flip, value := nextState(published, streakValue, streak, r, 2)
		streakValue, streak = r, streakAfter(streakValue, streak, r)
		if flip {
			published = value
			flips = append(flips, value)
		}
	}
	// one failure is a blip; two in a row flip to false; two passes flip back.
	assert.Equal(t, []bool{false, true}, flips)
}
