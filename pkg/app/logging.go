package app

import (
	"log/slog"
	"os"
	"strings"

	"github.com/infrapi/lib/pkg/config"
	"github.com/sirupsen/logrus"
)

// setupLogging makes every line of the process one object with the same
// members — time, level, msg — and the same level names, whichever logger
// wrote it: the engine's access log, logrus (the cluster library), or slog
// (the service and this package).
func setupLogging(app *config.AppConfig) {
	logrus.SetOutput(os.Stdout)
	logrus.SetLevel(app.AppLogLevel)
	if app.AppLogFormat == "json" {
		logrus.SetFormatter(&logrus.JSONFormatter{})
	}

	level := slog.LevelInfo
	switch app.AppLogLevel {
	case logrus.TraceLevel, logrus.DebugLevel:
		level = slog.LevelDebug
	case logrus.ErrorLevel:
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		// logrus's names: info, warning, error, debug.
		if a.Key == slog.LevelKey {
			name := strings.ToLower(a.Value.String())
			if name == "warn" {
				name = "warning"
			}
			return slog.String(slog.LevelKey, name)
		}
		return a
	}}
	if app.AppLogFormat == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, opts)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))
	}
}
