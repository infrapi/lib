package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// A Check tells whether this replica may lead a leader-mode role: nil means
// yes. Checks run on a timer, each under its own timeout, and every one must
// pass; see INFRAPI_CLUSTER_LEADER_CHECKS.
type Check interface {
	// Check returns nil when the condition holds, or why it does not.
	Check(ctx context.Context) error
	// String is how the check is named in logs, metrics and the status pages.
	String() string
}

// CheckFunc makes a Check of a function, for a condition only Go code can
// tell: name is how it is shown.
func CheckFunc(name string, fn func(ctx context.Context) error) Check { return funcCheck{name, fn} }

type funcCheck struct {
	name string
	fn   func(ctx context.Context) error
}

func (f funcCheck) Check(ctx context.Context) error { return f.fn(ctx) }
func (f funcCheck) String() string                  { return f.name }

// ParseCheck reads one check, written like a URL:
//
//	tcp://host:port      a TCP connection opens (telnet://host:port too)
//	http(s)://…          a GET answers 2xx; redirects are not followed
//	exec:/path arg …     the command exits 0 — no shell, arguments split on spaces
//	file:///path         the file exists
//	!<check>             the check fails, e.g. !file:///etc/infrapi/maintenance
func ParseCheck(s string) (Check, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "!"); ok {
		inner, err := ParseCheck(rest)
		if err != nil {
			return nil, err
		}
		return notCheck{inner}, nil
	}

	if rest, ok := strings.CutPrefix(s, "exec:"); ok {
		args := strings.Fields(rest)
		if len(args) == 0 {
			return nil, fmt.Errorf("cluster: leader check %q: no command", s)
		}
		return execCheck{raw: s, args: args}, nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("cluster: leader check %q: %w", s, err)
	}
	switch u.Scheme {
	case "tcp", "telnet":
		if _, _, err := net.SplitHostPort(u.Host); err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("cluster: leader check %q: expected %s://host:port", s, u.Scheme)
		}
		return tcpCheck{raw: s, addr: u.Host}, nil
	case "http", "https":
		if u.Host == "" {
			return nil, fmt.Errorf("cluster: leader check %q: no host", s)
		}
		return httpCheck{raw: s, url: u.String()}, nil
	case "file":
		if u.Host != "" || u.Path == "" {
			return nil, fmt.Errorf("cluster: leader check %q: expected file:///absolute/path", s)
		}
		return fileCheck{raw: s, path: u.Path}, nil
	default:
		return nil, fmt.Errorf("cluster: leader check %q: unknown kind, expected tcp://, telnet://, http(s)://, exec: or file://", s)
	}
}

// ParseChecks reads every check of a list, refusing the list on the first one
// that does not parse: a check that is silently dropped is a node that leads
// when it should not.
func ParseChecks(list []string) ([]Check, error) {
	checks := make([]Check, 0, len(list))
	for _, s := range list {
		c, err := ParseCheck(s)
		if err != nil {
			return nil, err
		}
		checks = append(checks, c)
	}
	return checks, nil
}

type tcpCheck struct{ raw, addr string }

func (c tcpCheck) String() string { return c.raw }

func (c tcpCheck) Check(ctx context.Context) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

type httpCheck struct{ raw, url string }

func (c httpCheck) String() string { return c.raw }

// checkClient follows no redirect: a 302 to a login page is not a healthy
// dependency, and a check is not the place to wander off to another host.
var checkClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func (c httpCheck) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	resp, err := checkClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	return nil
}

type execCheck struct {
	raw  string
	args []string
}

func (c execCheck) String() string { return c.raw }

func (c execCheck) Check(ctx context.Context) error {
	// #nosec G204 -- the command is the operator's, from the configuration.
	out, err := exec.CommandContext(ctx, c.args[0], c.args[1:]...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			if len(msg) > 200 {
				msg = msg[:200] + "…"
			}
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

type fileCheck struct{ raw, path string }

func (c fileCheck) String() string { return c.raw }

func (c fileCheck) Check(context.Context) error {
	_, err := os.Stat(c.path)
	return err
}

type notCheck struct{ inner Check }

func (c notCheck) String() string { return "!" + c.inner.String() }

func (c notCheck) Check(ctx context.Context) error {
	if err := c.inner.Check(ctx); err != nil {
		// A timeout is not an answer: the condition is unknown, not false.
		if errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	}
	return errors.New("holds")
}

// runChecks runs every check under its own timeout and returns the first
// failure, named after its check — or nil when all pass.
func runChecks(ctx context.Context, checks []Check, timeout time.Duration) (failed Check, err error) {
	for _, c := range checks {
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		err := c.Check(checkCtx)
		cancel()
		if err != nil {
			return c, fmt.Errorf("%s: %w", c, err)
		}
	}
	return nil, nil
}
