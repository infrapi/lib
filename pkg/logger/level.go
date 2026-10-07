package logger

import (
	"fmt"

	"go.uber.org/zap/zapcore"
)

// parseLevel maps a level name onto the zap level that filters at it.
// Recognised, case-sensitively: "trace", "debug", "info", "warn" (or
// "warning"), "error". Empty is "info", INFRAPI_APP_LOG_LEVEL's own default.
//
// zap has no trace level; "trace" is accepted for parity with
// INFRAPI_APP_LOG_LEVEL's validator (trace debug info error) and filters at
// debug, zap's lowest. A line logged at trace-equivalent detail should use
// [Logger.Debug] - there is nothing finer to fall back to.
//
// Unexported: the zapcore.Level it returns is exactly the zap dependency
// this package keeps out of its API, so the only caller is [New].
func parseLevel(level string) (zapcore.Level, error) {
	switch level {
	case "", "info":
		return zapcore.InfoLevel, nil
	case "trace", "debug":
		return zapcore.DebugLevel, nil
	case "warn", "warning":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("logger: unknown level %q", level)
	}
}

// encoderConfig is shared by the JSON and console encoders, so a line carries
// the same field names and a level reads the same name either way.
func encoderConfig() zapcore.EncoderConfig {
	cfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "component",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    encodeLevel,
		EncodeTime:     zapcore.RFC3339NanoTimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	return cfg
}

// encodeLevel writes zap's own level names, except warn: pkg/server's JSON
// access log and pkg/app's slog setup both write logrus's name for it,
// "warning", so that an operator filtering a collector on level="warning"
// finds every warning line in the process, not just the ones logrus wrote.
func encodeLevel(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	if l == zapcore.WarnLevel {
		enc.AppendString("warning")
		return
	}
	zapcore.LowercaseLevelEncoder(l, enc)
}
