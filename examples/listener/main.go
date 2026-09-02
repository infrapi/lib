package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/infrapi/lib/pkg/config"
	"github.com/infrapi/lib/pkg/listener"
	infraserver "github.com/infrapi/lib/pkg/server"
)

func main() {
	fmt.Println("=== InfraPi Listener Package Examples ===")
	fmt.Println()

	// Example 1: Open the listener described by the configuration
	example1()

	// Example 2: Serve the server package engine on that listener
	example2()

	// Example 3: Ask for systemd socket activation without systemd
	example3()

	// Example 4: Reject an unknown listen type
	example4()
}

// loadConfig is a helper that loads AppConfig from the bundled app.env file.
func loadConfig() *config.AppConfig {
	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/listener/app.env"); err != nil {
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

// Example 1: Open the socket the configuration asks for.
func example1() {
	fmt.Println("--- Example 1: TCP Listener From Configuration ---")

	appConfig := loadConfig()
	// Port 0 lets the kernel pick a free port so the example runs anywhere.
	appConfig.AppListenAddress = "127.0.0.1:0"

	ln, err := listener.Listen(appConfig)
	if err != nil {
		log.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	fmt.Printf("Listen type : %s\n", appConfig.AppListenType)
	fmt.Printf("Network     : %s\n", ln.Addr().Network())
	fmt.Printf("Address     : %s\n\n", ln.Addr())
}

// Example 2: The listener and the server package together, which is how a
// service is wired: config builds both, http.Server glues them.
func example2() {
	fmt.Println("--- Example 2: Serving the Gin Engine on the Listener ---")

	appConfig := loadConfig()
	appConfig.AppListenAddress = "127.0.0.1:0"

	ln, err := listener.Listen(appConfig)
	if err != nil {
		log.Fatalf("Listen: %v", err)
	}

	gin.SetMode(gin.ReleaseMode)
	engine, err := infraserver.NewServerGin(appConfig)
	if err != nil {
		log.Fatalf("NewServerGin: %v", err)
	}

	engine.GET("/api/v1/widgets", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"widgets": []string{"foo", "bar"}})
	})

	srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	fmt.Printf("Serving on %s\n", ln.Addr())

	for _, path := range []string{"/-/metadata", "/api/v1/widgets"} {
		resp, err := http.Get(fmt.Sprintf("http://%s%s", ln.Addr(), path))
		if err != nil {
			log.Fatalf("GET %s: %v", path, err)
		}
		fmt.Printf("GET %-17s → %d\n", path, resp.StatusCode)
		_ = resp.Body.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
	fmt.Println("Server shut down cleanly.")
	fmt.Println()
}

// Example 3: Under systemd the socket arrives on descriptor 3, and the process
// is told so through LISTEN_PID and LISTEN_FDS. Without them, Listen refuses.
func example3() {
	fmt.Println("--- Example 3: Systemd Socket Activation ---")

	appConfig := loadConfig()
	appConfig.AppListenType = "systemd"

	if _, err := listener.Listen(appConfig); err != nil {
		fmt.Printf("Expected error outside of systemd: %v\n\n", err)
	}
}

// Example 4: Anything but tcp or systemd is rejected.
func example4() {
	fmt.Println("--- Example 4: Unknown Listen Type ---")

	appConfig := loadConfig()
	appConfig.AppListenType = "udp"

	if _, err := listener.Listen(appConfig); err != nil {
		fmt.Printf("Expected error: %v\n", err)
	}
}
