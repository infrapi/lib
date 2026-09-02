package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	infrahttp "github.com/infrapi/lib/pkg/http"
)

func main() {
	fmt.Println("=== InfraPi HTTP Package Examples ===")
	fmt.Println()

	srv := startServer()
	defer func() { _ = srv.Close() }()

	// Wait for the server to be ready
	time.Sleep(100 * time.Millisecond)

	// Example 1: Basic GET request
	example1()

	// Example 2: HTTP methods (POST, PUT, DELETE, PATCH)
	example2()

	// Example 3: Custom headers and user agent
	example3()

	// Example 4: Error handling (4xx / 5xx / wrong server)
	example4()

	// Example 5: Retry on transient failures
	example5()

	// Example 6: Hedging for low-latency reads
	example6()

	// Example 7: mTLS client certificate (content)
	example7()
}

// startServer launches a local HTTP server that backs all examples.
func startServer() *http.Server {
	mux := http.NewServeMux()

	// Echo handler registered for specific paths
	echo := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]string{
			"method": r.Method,
			"path":   r.URL.Path,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
	for _, path := range []string{
		"/api/v1/myapp/status",
		"/api/v1/myapp/config",
		"/api/v1/myapp/items",
		"/api/v1/myapp/events",
		"/api/v1/myapp/ping",
	} {
		mux.HandleFunc(path, echo)
	}

	// Slow endpoint (simulates tail latency)
	mux.HandleFunc("/api/v1/myapp/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = fmt.Fprintln(w, `{"status":"slow"}`)
	})

	// Endpoint that fails the first N calls then succeeds
	var retryCounter int32
	mux.HandleFunc("/api/v1/myapp/flaky", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&retryCounter, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintln(w, `{"error":"transient"}`)
			return
		}
		_, _ = fmt.Fprintln(w, `{"status":"ok"}`)
	})

	srv := &http.Server{Addr: "127.0.0.1:18080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.ListenAndServe() //nolint:errcheck
	return srv
}

// newClient is a helper that builds a client pointed at the local example server.
func newClient(opts *infrahttp.ClientOptions) *infrahttp.Client {
	opts.Server = "127.0.0.1"
	opts.Protocol = "http"
	opts.Port = 18080
	opts.Application = "myapp"

	client, err := infrahttp.NewClient(opts)
	if err != nil {
		log.Fatalf("newClient: %v", err)
	}
	return client
}

// Example 1: Basic GET request
func example1() {
	fmt.Println("--- Example 1: Basic GET Request ---")

	client := newClient(&infrahttp.ClientOptions{
		Token:      "my-infrapi-token",
		APIVersion: "v1",
	})

	resp, err := client.Get("status", nil)
	if err != nil {
		log.Printf("GET error: %v", err)
		return
	}

	fmt.Printf("Status code : %d\n", resp.Code)
	fmt.Printf("Body        : %s\n\n", resp.Body)
}

// Example 2: HTTP methods (POST, PUT, DELETE, PATCH, HEAD)
func example2() {
	fmt.Println("--- Example 2: HTTP Methods ---")

	client := newClient(&infrahttp.ClientOptions{})

	payload := []byte(`{"name":"widget","value":42}`)

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD"} {
		resp, err := client.Do(method, "items", &infrahttp.RequestOptions{Data: payload})
		if err != nil {
			log.Printf("%s error: %v", method, err)
			continue
		}
		fmt.Printf("%-6s  code=%d  body=%s", method, resp.Code, resp.Body)
		if len(resp.Body) == 0 {
			fmt.Println()
		}
	}
	fmt.Println()
}

// Example 3: Custom headers and user agent
func example3() {
	fmt.Println("--- Example 3: Custom Headers and User Agent ---")

	client := newClient(&infrahttp.ClientOptions{
		UserAgent: "my-service/2.0",
		Headers: map[string]string{
			"X-Correlation-ID": "abc-123",
			"X-Tenant":         "acme",
		},
	})

	fmt.Printf("User-Agent header : %s\n", client.RestyClient.Header().Get("User-Agent"))
	fmt.Printf("X-Correlation-ID  : %s\n", client.RestyClient.Header().Get("X-Correlation-ID"))
	fmt.Printf("X-Tenant          : %s\n\n", client.RestyClient.Header().Get("X-Tenant"))

	resp, err := client.Get("config", nil)
	if err != nil {
		log.Printf("GET error: %v", err)
		return
	}
	fmt.Printf("Response: %s\n\n", resp.Body)
}

// Example 4: Error handling (404, bad server)
func example4() {
	fmt.Println("--- Example 4: Error Handling ---")

	client := newClient(&infrahttp.ClientOptions{RetryCount: 0})

	// 404 — path does not exist on the router but the mux returns 404
	resp, err := client.Get("nonexistent/deep/path", nil)
	if err != nil {
		fmt.Printf("Expected 404 error: %v (code=%d)\n", err, resp.Code)
	}

	// Wrong server — connection refused
	_, err = infrahttp.NewClient(&infrahttp.ClientOptions{
		Server:      "127.0.0.1",
		Protocol:    "http",
		Port:        19999, // nothing listening here
		Application: "myapp",
		RetryCount:  0,
	})
	if err != nil {
		log.Printf("NewClient error: %v", err)
		return
	}
	badClient, _ := infrahttp.NewClient(&infrahttp.ClientOptions{
		Server:        "127.0.0.1",
		Protocol:      "http",
		Port:          19999,
		Application:   "myapp",
		RetryCount:    0,
		GlobalTimeout: 1,
	})
	_, err = badClient.Get("ping", nil)
	if err != nil {
		fmt.Printf("Expected connection error: %v\n\n", err)
	}
}

// Example 5: Retry on transient 5xx failures
func example5() {
	fmt.Println("--- Example 5: Retry on Transient Failures ---")

	// The /flaky endpoint returns 500 for the first 2 calls, then 200.
	// With RetryCount=3 and a short wait, the client will succeed automatically.
	client := newClient(&infrahttp.ClientOptions{
		RetryCount:       3,
		RetryWaitTime:    50 * time.Millisecond,
		RetryMaxWaitTime: 500 * time.Millisecond,
	})

	resp, err := client.Get("flaky", nil)
	if err != nil {
		log.Printf("Retry example error: %v", err)
		return
	}
	fmt.Printf("Succeeded after retries: code=%d body=%s\n\n", resp.Code, resp.Body)
}

// Example 6: Hedging — send up to 3 concurrent requests, return the fastest
func example6() {
	fmt.Println("--- Example 6: Hedging for Low-Latency Reads ---")

	// Without hedging the /slow endpoint would take 2s.
	// With hedging we fire 3 requests staggered by 50ms; the first one to
	// respond (or a later one if the first is still pending) wins.
	// Here we use the normal /status endpoint so the example finishes quickly.
	client := newClient(&infrahttp.ClientOptions{
		Hedging: &infrahttp.HedgingOptions{
			Enabled:      true,
			Delay:        50 * time.Millisecond,
			UpTo:         3,
			MaxPerSecond: 10,
		},
	})

	start := time.Now()
	resp, err := client.Get("status", nil)
	if err != nil {
		log.Printf("Hedging error: %v", err)
		return
	}
	fmt.Printf("Response in %v: code=%d body=%s", time.Since(start).Round(time.Millisecond), resp.Code, resp.Body)
	fmt.Println()

	// Demonstrate NonReadOnlyAllowed — hedge a POST (use with care: may cause
	// duplicate writes if the server is not idempotent).
	hedgedPost := newClient(&infrahttp.ClientOptions{
		Hedging: &infrahttp.HedgingOptions{
			Enabled:            true,
			Delay:              50 * time.Millisecond,
			UpTo:               2,
			NonReadOnlyAllowed: true,
		},
	})
	resp, err = hedgedPost.Post("events", &infrahttp.RequestOptions{Data: []byte(`{"event":"ping"}`)})
	if err != nil {
		log.Printf("Hedged POST error: %v", err)
		return
	}
	fmt.Printf("Hedged POST: code=%d body=%s\n\n", resp.Code, resp.Body)
}

// Example 7: mTLS — provide a client certificate via PEM content
func example7() {
	fmt.Println("--- Example 7: mTLS Client Certificate ---")

	// In a real scenario you would load cert/key from Vault, a secret manager,
	// or the filesystem. Here we show both approaches.

	// Approach A: provide PEM bytes directly (e.g. from Vault)
	_, err := infrahttp.NewClient(&infrahttp.ClientOptions{
		Server:            "internal.example.com",
		Application:       "myapp",
		Protocol:          "https",
		Port:              443,
		ClientCertContent: []byte("-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----"),
		ClientKeyContent:  []byte("-----BEGIN RSA PRIVATE KEY-----\n...\n-----END RSA PRIVATE KEY-----"),
	})
	if err != nil {
		// Expected — the placeholder cert bytes above are not valid
		fmt.Printf("Approach A (content): client creation attempted, error expected with placeholder: %v\n", err)
	}

	// Approach B: reference cert/key files on disk
	_, err = infrahttp.NewClient(&infrahttp.ClientOptions{
		Server:         "internal.example.com",
		Application:    "myapp",
		Protocol:       "https",
		Port:           443,
		ClientCertPath: "/etc/ssl/myapp/client.crt",
		ClientKeyPath:  "/etc/ssl/myapp/client.key",
	})
	if err != nil {
		// Expected — the files do not exist in this environment
		fmt.Printf("Approach B (path)   : client creation attempted, error expected without cert files: %v\n", err)
	}

	fmt.Println()
}
