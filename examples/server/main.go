package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/config"
	infraserver "github.com/infrapi/lib/pkg/server"
)

func main() {
	fmt.Println("=== InfraPi Server Package Examples ===")
	fmt.Println()

	// Example 1: Basic server setup from a .env file
	example1()

	// Example 2: Inspect the server configuration and registered routes
	example2()

	// Example 3: Add custom routes to the gin engine
	example3()

	// Example 4: Override CORS and trust settings with custom config
	example4()

	// Example 5: Start the server and perform a live request (graceful shutdown)
	example5()
}

// loadConfig is a helper that loads AppConfig from the bundled app.env file.
func loadConfig() *config.AppConfig {
	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/server/app.env"); err != nil {
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

// Example 1: Basic server setup — create a gin.Engine from an AppConfig.
func example1() {
	fmt.Println("--- Example 1: Basic Server Setup ---")

	appConfig := loadConfig()
	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	fmt.Printf("Engine created: %T\n", engine)
	fmt.Printf("App: %s  Platform: %s  Region: %s  Location: %s\n\n",
		appConfig.AppName, appConfig.AppPlatform, appConfig.AppRegion, appConfig.AppLocation)
}

// Example 2: Inspect registered routes.
func example2() {
	fmt.Println("--- Example 2: Registered Routes ---")

	engine, err := infraserver.NewServerGin(loadConfig())
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	for _, r := range engine.Routes() {
		fmt.Printf("  %-7s %s\n", r.Method, r.Path)
	}
	fmt.Println()
}

// Example 3: Extend the server with application-specific routes.
func example3() {
	fmt.Println("--- Example 3: Custom Routes ---")

	appConfig := loadConfig()
	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	// Add application routes on top of the pre-configured engine.
	v1 := engine.Group("/api/v1")
	{
		v1.GET("/widgets", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"widgets": []string{"foo", "bar"}})
		})
		v1.POST("/widgets", func(c *gin.Context) {
			c.JSON(http.StatusCreated, gin.H{"created": true})
		})
	}

	fmt.Println("All routes after adding application endpoints:")
	for _, r := range engine.Routes() {
		fmt.Printf("  %-7s %s\n", r.Method, r.Path)
	}
	fmt.Println()
}

// Example 4: Customise CORS and trusted proxies before building the server.
func example4() {
	fmt.Println("--- Example 4: Custom CORS and Trusted Proxies ---")

	appConfig := loadConfig()

	// Override CORS and proxy settings programmatically.
	appConfig.AppCorsAllowOrigins = []string{"https://app.example.com", "https://admin.example.com"}
	appConfig.AppCorsAllowMethods = []string{"GET", "POST", "DELETE"}
	appConfig.AppTrustedProxies = []string{"10.0.0.0/8"}

	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	fmt.Printf("CORS origins : %v\n", appConfig.AppCorsAllowOrigins)
	fmt.Printf("CORS methods : %v\n", appConfig.AppCorsAllowMethods)
	fmt.Printf("Trusted proxies: %v\n", appConfig.AppTrustedProxies)
	fmt.Printf("Engine ready: %T\n\n", engine)
}

// Example 5: Start the server, hit /-/metadata, then shut it down gracefully.
func example5() {
	fmt.Println("--- Example 5: Live Server with Graceful Shutdown ---")

	appConfig := loadConfig()

	// Use a random free port so the example works in any environment.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	appConfig.AppListenAddress = addr

	gin.SetMode(gin.ReleaseMode)
	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}

	// Start in background.
	go func() { _ = srv.Serve(listener) }()
	fmt.Printf("Server listening on %s\n", addr)

	// Hit the /-/metadata endpoint.
	resp, err := http.Get(fmt.Sprintf("http://%s/-/metadata", addr))
	if err != nil {
		log.Fatalf("GET /-/metadata: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	fmt.Printf("GET /-/metadata → %d\n", resp.StatusCode)

	// Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
	fmt.Println("Server shut down cleanly.")
}
