package main

import (
	"fmt"
	"log"
	"os"

	"github.com/infrapi/lib/pkg/config"
	"github.com/infrapi/lib/pkg/logger"
)

func main() {
	fmt.Println("=== InfraPi Logger Package Examples ===")
	fmt.Println()

	example1()
	example2()
	example3()
	example4()
}

// loadConfig is a helper that loads AppConfig from the bundled app.env file.
func loadConfig() *config.AppConfig {
	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/logger/app.env"); err != nil {
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

// Example 1: Build a logger from AppConfig, the same INFRAPI_APP_LOG_LEVEL and
// INFRAPI_APP_LOG_FORMAT every other package already reads. Nothing here
// imports go.uber.org/zap - logger.String is this package's own.
func example1() {
	fmt.Println("--- Example 1: A Logger From AppConfig ---")

	appConfig := loadConfig()
	l, err := logger.New(logger.Options{
		Level:  appConfig.AppLogLevel.String(),
		Format: appConfig.AppLogFormat,
		Name:   "logger-example",
	})
	if err != nil {
		log.Fatalf("New: %v", err)
	}
	defer func() { _ = l.Sync() }()

	l.Info("service starting", logger.String("platform", appConfig.AppPlatform))
	fmt.Println()
}

// Example 2: One JSON line per record, the schema pkg/server's access log and
// infrapi/app's slog setup already write - time, level, msg, plus fields.
func example2() {
	fmt.Println("--- Example 2: JSON Output ---")

	l, err := logger.New(logger.Options{Format: logger.FormatJSON, Name: "worker"})
	if err != nil {
		log.Fatalf("New: %v", err)
	}

	l.Info("job started", logger.String("queue", "emails"), logger.Int("workers", 3))
	l.Warn("queue backing up", logger.String("queue", "emails")) // written with level="warning"
	l.Error("job failed", logger.Err(fmt.Errorf("dial tcp: timeout")))
	fmt.Println()
}

// Example 3: A level set above a line's own drops it before it is written.
func example3() {
	fmt.Println("--- Example 3: Level Filtering ---")

	l, err := logger.New(logger.Options{Level: "error", Format: logger.FormatJSON})
	if err != nil {
		log.Fatalf("New: %v", err)
	}

	l.Info("not printed: below the error threshold")
	l.Error("printed: at the threshold")
	fmt.Println()
}

// Example 4: SetDefault is how one logger, built once in main, reaches every
// package that calls L afterwards - no logger argument threaded through
// server's own constructors. Infof is the printf-style alternative to typed
// fields, for a call site that wants it.
func example4() {
	fmt.Println("--- Example 4: The Package-Level Default ---")

	l, err := logger.New(logger.Options{Format: logger.FormatJSON, Name: "server"})
	if err != nil {
		log.Fatalf("New: %v", err)
	}
	logger.SetDefault(l)
	defer func() { _ = logger.Sync() }()

	logger.L().Info("handled by whatever called SetDefault")
	logger.L().Infof("request answered with status %d", 200)
}
