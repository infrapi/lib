// Package demo holds the handful of lines every cluster example repeats:
// loading the dotenv next to it, keeping dskit's own logging quiet, and
// starting a cluster. Everything specific to an example stays in its own
// main.go, so each one reads top to bottom on its own.
package demo

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/config"
)

// Config reads the dotenv bundled next to the example being run. Each example
// directory carries its own, so running one tells you exactly which keys it
// needs. INFRAPI_CONFIG_DOTENV_FILE wins if the caller set it.
func Config(path string) (*config.AppConfig, *config.ClusterConfig) {
	if os.Getenv("INFRAPI_CONFIG_DOTENV_FILE") == "" {
		if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", path); err != nil {
			log.Fatalf("demo: setenv: %v", err)
		}
	}

	vars, err := config.NewConfig()
	if err != nil {
		log.Fatalf("demo: NewConfig: %v", err)
	}

	appConfig, err := vars.GetAppConfig()
	if err != nil {
		log.Fatalf("demo: GetAppConfig: %v", err)
	}

	clusterConfig, err := vars.GetClusterConfig()
	if err != nil {
		log.Fatalf("demo: GetClusterConfig: %v", err)
	}

	return appConfig, clusterConfig
}

// Logger keeps dskit's chatter out of the example output. Raise the level in
// the dotenv to watch the gossip and the ring at work.
func Logger(level logrus.Level) *logrus.Logger {
	logger := logrus.New()
	logger.SetLevel(level)
	return logger
}

// Start builds a cluster and starts it, and returns the function that stops it.
//
// The registerer is left alone on purpose, so the package uses the default one
// and the cluster's metrics show up on /-/metrics where an operator looks for
// them. Every example here builds exactly one cluster per process, which is
// what makes that safe: dskit's DNS collectors carry no label that would tell
// two clusters apart, so a process building several has to give each its own
// registry.
func Start(app *config.AppConfig, cfg *config.ClusterConfig, opts cluster.Options) (*cluster.Cluster, func()) {
	if opts.Logger == nil {
		opts.Logger = Logger(app.AppLogLevel)
	}

	c, err := cluster.New(app, cfg, opts)
	if err != nil {
		log.Fatalf("cluster.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Start(ctx); err != nil {
		log.Fatalf("cluster.Start: %v", err)
	}

	return c, func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Minute)
		defer stopCancel()

		if err := c.Stop(stopCtx); err != nil {
			log.Fatalf("cluster.Stop: %v", err)
		}
	}
}
