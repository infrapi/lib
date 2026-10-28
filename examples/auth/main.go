package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	infraauth "github.com/infrapi/lib/pkg/auth"
	"github.com/infrapi/lib/pkg/config"
	infraserver "github.com/infrapi/lib/pkg/server"
)

func main() {
	fmt.Println("=== InfraPi Auth Package Examples ===")
	fmt.Println()

	authSvc, calls := startAuthStub()
	defer authSvc.Close()

	// Example 1: Call the authentication service directly through the client.
	example1(authSvc.URL)

	// Example 2: Gate every route a service adds with the middleware.
	example2(authSvc.URL)

	// Example 3: Cache Verify results to skip the HTTP round trip.
	example3(authSvc.URL, calls)
}

// startAuthStub stands in for a real authentication service's POST
// /oauth2/userinfo endpoint: it accepts "valid-token" and refuses
// everything else, exactly as that service's own bearer-auth middleware
// does. The returned counter tracks how many requests actually reached it.
func startAuthStub() (*httptest.Server, *atomic.Int64) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"jdoe","provider":"okta","authorization":"idm",` +
			`"groups":["sre","sandbox-apps"],"email":"jdoe@example.com"}`))
	}))
	return srv, &calls
}

// Example 1: resolve a bearer token to the identity the authentication
// service attaches to it.
func example1(authURL string) {
	fmt.Println("--- Example 1: Verify ---")

	client, err := infraauth.NewClient(infraauth.ClientOptions{BaseURL: authURL})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}

	user, err := client.Verify(context.Background(), "valid-token")
	if err != nil {
		log.Fatalf("Verify: %v", err)
	}
	fmt.Printf("sub=%s provider=%s groups=%v email=%s\n\n",
		user.Subject, user.Provider, user.Groups, user.Attributes["email"])
}

// Example 2: wire the client into a pkg/server engine with WithAuth, so
// every route the service adds goes through the authentication service
// first.
func example2(authURL string) {
	fmt.Println("--- Example 2: WithAuth middleware ---")

	appConfig := loadConfig()
	client, err := infraauth.NewClient(infraauth.ClientOptions{BaseURL: authURL})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}

	engine, err := infraserver.NewServerGin(appConfig, infraserver.WithAuth(client.Middleware()))
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	engine.GET("/widgets", func(c *gin.Context) {
		user, _ := infraauth.GetUser(c)
		c.JSON(http.StatusOK, gin.H{"owner": user.Subject})
	})

	srv := httptest.NewServer(engine)
	defer srv.Close()

	// No token: the engine's own /-/healthz still answers (registered
	// before WithAuth), the service's /widgets does not.
	fmt.Printf("GET /-/healthz (no token) -> %d\n", mustGet(srv.URL+"/-/healthz", ""))
	fmt.Printf("GET /widgets   (no token) -> %d\n", mustGet(srv.URL+"/widgets", ""))
	fmt.Printf("GET /widgets   (bad token) -> %d\n", mustGet(srv.URL+"/widgets", "wrong-token"))
	fmt.Printf("GET /widgets   (valid token) -> %d\n\n", mustGet(srv.URL+"/widgets", "valid-token"))
}

// Example 3: Verify caches by default (infraauth.DefaultCacheTTL, 1h), so
// repeated calls for the same token stop reaching the authentication
// service; a negative CacheTTL opts back out.
func example3(authURL string, calls *atomic.Int64) {
	fmt.Println("--- Example 3: Caching Verify results ---")

	cached, err := infraauth.NewClient(infraauth.ClientOptions{BaseURL: authURL})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	uncached, err := infraauth.NewClient(infraauth.ClientOptions{BaseURL: authURL, CacheTTL: -1})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}

	before := calls.Load()
	for range 3 {
		if _, err := cached.Verify(context.Background(), "valid-token"); err != nil {
			log.Fatalf("Verify: %v", err)
		}
	}
	fmt.Printf("default client,  3 Verify calls -> %d reached the service\n", calls.Load()-before)

	before = calls.Load()
	for range 3 {
		if _, err := uncached.Verify(context.Background(), "valid-token"); err != nil {
			log.Fatalf("Verify: %v", err)
		}
	}
	fmt.Printf("CacheTTL: -1,    3 Verify calls -> %d reached the service\n\n", calls.Load()-before)
}

func mustGet(url, token string) int {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		log.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func loadConfig() *config.AppConfig {
	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/auth/app.env"); err != nil {
		log.Fatalf("loadConfig: setenv: %v", err)
	}
	cfg, err := config.NewConfig()
	if err != nil {
		log.Fatalf("loadConfig: NewConfig: %v", err)
	}
	appConfig, err := cfg.GetAppConfig()
	if err != nil {
		log.Fatalf("loadConfig: GetAppConfig: %v", err)
	}
	return appConfig
}
