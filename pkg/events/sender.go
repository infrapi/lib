package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/infrapi/lib/pkg/logger"
)

// Sender sends events somewhere: Kayra, or the service's log.
type Sender interface {
	// Create sends evs, all of namespace ("" the cluster's), and returns
	// their ids, in order: "" for an event a log keeps.
	Create(ctx context.Context, namespace string, evs []Event) ([]string, error)
	// UpdateSeries changes the series of the event id of namespace.
	UpdateSeries(ctx context.Context, namespace, id string, s Series) error
}

// RetryAfter is a Sender's error when the receiver asks to wait.
type RetryAfter struct {
	Wait time.Duration
}

func (r RetryAfter) Error() string { return fmt.Sprintf("events: asked to wait %s", r.Wait) }

// Kayra sends events to the Kayra at baseURL (the URL the service reaches it
// by, its auth URL), its events group found in its /.well-known/kayra.json,
// with token's tokens: the service's own identity's
// (auth.ClientCredentials.Token).
func Kayra(baseURL string, token func(ctx context.Context) (string, error), client *http.Client) Sender {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &kayra{base: strings.TrimRight(baseURL, "/"), token: token, http: client}
}

type kayra struct {
	base  string
	token func(ctx context.Context) (string, error)
	http  *http.Client

	mu   sync.Mutex
	root string // /apis/events.<domain>/v1, once discovered
}

func (k *kayra) eventsRoot(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.root != "" {
		return k.root, nil
	}
	var doc struct {
		APIs map[string]string `json:"apis"`
	}
	if _, err := k.do(ctx, http.MethodGet, k.base+"/.well-known/kayra.json", nil, &doc, false); err != nil {
		return "", err
	}
	if doc.APIs["events"] == "" {
		return "", fmt.Errorf("events: %s serves no events group: a Kayra before events", k.base)
	}
	k.root = doc.APIs["events"]
	return k.root, nil
}

func (k *kayra) path(ctx context.Context, namespace string) (string, error) {
	root, err := k.eventsRoot(ctx)
	if err != nil {
		return "", err
	}
	if namespace == "" {
		return k.base + root + "/events", nil
	}
	return k.base + root + "/namespaces/" + namespace + "/events", nil
}

func (k *kayra) Create(ctx context.Context, namespace string, evs []Event) ([]string, error) {
	url, err := k.path(ctx, namespace)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if _, err := k.do(ctx, http.MethodPost, url, map[string][]Event{"items": evs}, &out, true); err != nil {
		return nil, err
	}
	ids := make([]string, len(evs))
	for i := range out.Items {
		if i < len(ids) {
			ids[i] = out.Items[i].ID
		}
	}
	return ids, nil
}

func (k *kayra) UpdateSeries(ctx context.Context, namespace, id string, s Series) error {
	url, err := k.path(ctx, namespace)
	if err != nil {
		return err
	}
	_, err = k.do(ctx, http.MethodPatch, url+"/"+id, map[string]Series{"series": s}, nil, true)
	return err
}

func (k *kayra) do(ctx context.Context, method, url string, body, out any, auth bool) (int, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		token, err := k.token(ctx)
		if err != nil {
			return 0, fmt.Errorf("events: a token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return resp.StatusCode, RetryAfter{Wait: time.Duration(max(wait, 1)) * time.Second}
	}
	if resp.StatusCode/100 != 2 {
		var p struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &p)
		return resp.StatusCode, fmt.Errorf("events: %s %s: %d %s", method, url, resp.StatusCode, p.Detail)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("events: %s: %w", url, err)
		}
	}
	return resp.StatusCode, nil
}

// Log writes events to log, a line each, the default logger's when nil
// (logger.L): where they go without Kayra, and while it cannot be reached.
func Log(log *logger.Logger) Sender {
	return logSender{log: log}
}

type logSender struct{ log *logger.Logger }

func (l logSender) Create(_ context.Context, namespace string, evs []Event) ([]string, error) {
	log := l.log
	if log == nil {
		log = logger.L()
	}
	for _, e := range evs {
		log.Info("event", logger.String("type", e.Type), logger.String("reason", e.Reason),
			logger.String("api", e.Regarding.API), logger.String("namespace", namespace),
			logger.String("resource", e.Regarding.Resource), logger.String("name", e.Regarding.Name),
			logger.String("note", e.Note), logger.String("user", e.User), logger.String("request_id", e.RequestID))
	}
	return make([]string, len(evs)), nil
}

func (l logSender) UpdateSeries(context.Context, string, string, Series) error { return nil }
