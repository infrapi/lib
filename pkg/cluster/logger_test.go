package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogger returns a real logrus logger writing JSON into a buffer, and a
// decoder for the lines it produced. No mock: the thing under test is exactly
// how logrus renders what the adapter hands it.
func captureLogger(t *testing.T, lvl logrus.Level) (*logrus.Logger, func() []map[string]any) {
	t.Helper()

	buf := &bytes.Buffer{}
	logger := logrus.New()
	logger.SetOutput(buf)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(lvl)

	return logger, func() []map[string]any {
		var lines []map[string]any
		for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
			if len(raw) == 0 {
				continue
			}
			var line map[string]any
			require.NoError(t, json.Unmarshal(raw, &line))
			lines = append(lines, line)
		}
		return lines
	}
}

func TestNewGoKitLogger_levels(t *testing.T) {
	for name, tc := range map[string]struct {
		log       func(log.Logger)
		wantLevel string
	}{
		"debug":                 {func(l log.Logger) { _ = level.Debug(l).Log("msg", "m") }, "debug"},
		"info":                  {func(l log.Logger) { _ = level.Info(l).Log("msg", "m") }, "info"},
		"warn":                  {func(l log.Logger) { _ = level.Warn(l).Log("msg", "m") }, "warning"},
		"error":                 {func(l log.Logger) { _ = level.Error(l).Log("msg", "m") }, "error"},
		"unlevelled is info":    {func(l log.Logger) { _ = l.Log("msg", "m") }, "info"},
		"unknown level is info": {func(l log.Logger) { _ = l.Log("level", "fatal", "msg", "m") }, "info"},
		// Neither a string nor a Stringer: dskit should never do this, but the
		// adapter must not drop the line if something does.
		"unreadable level is info": {func(l log.Logger) { _ = l.Log("level", 42, "msg", "m") }, "info"},
	} {
		t.Run(name, func(t *testing.T) {
			logger, lines := captureLogger(t, logrus.DebugLevel)
			tc.log(NewGoKitLogger(logger))

			got := lines()
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantLevel, got[0]["level"])
			assert.Equal(t, "m", got[0]["msg"])
		})
	}
}

// dskit names its error field "err" everywhere. Renaming it to logrus' own
// "error" would break every query written against dskit's documentation.
func TestNewGoKitLogger_keeps_the_err_field_name(t *testing.T) {
	logger, lines := captureLogger(t, logrus.DebugLevel)

	_ = level.Error(NewGoKitLogger(logger)).Log("msg", "join failed", "err", errors.New("boom"), "attempt", 3)

	got := lines()
	require.Len(t, got, 1)
	assert.Equal(t, "join failed", got[0]["msg"])
	assert.Equal(t, "boom", got[0]["err"])
	assert.EqualValues(t, 3, got[0]["attempt"])
}

// A key with no value must not panic: dskit calls Log from goroutines this
// package does not own.
func TestNewGoKitLogger_odd_keyvals(t *testing.T) {
	logger, lines := captureLogger(t, logrus.DebugLevel)

	_ = NewGoKitLogger(logger).Log("msg", "m", "dangling")

	got := lines()
	require.Len(t, got, 1)
	assert.Equal(t, missingValue, got[0]["dangling"])
}

func TestNewGoKitLogger_non_string_key(t *testing.T) {
	logger, lines := captureLogger(t, logrus.DebugLevel)

	_ = NewGoKitLogger(logger).Log(42, "answer", "msg", "m")

	got := lines()
	require.Len(t, got, 1)
	assert.Equal(t, "answer", got[0]["42"])
}

// log.With can stack messages. The last is the line being logged; the earlier
// ones are context and are kept rather than dropped.
func TestNewGoKitLogger_stacked_messages(t *testing.T) {
	logger, lines := captureLogger(t, logrus.DebugLevel)

	outer := log.With(NewGoKitLogger(logger), "msg", "outer")
	_ = outer.Log("msg", "inner")

	got := lines()
	require.Len(t, got, 1)
	assert.Equal(t, "inner", got[0]["msg"])
	assert.Equal(t, "outer", got[0]["msg_1"])
}

// Filtering belongs to logrus, not to the adapter.
func TestNewGoKitLogger_logrus_filters(t *testing.T) {
	logger, lines := captureLogger(t, logrus.InfoLevel)

	_ = level.Debug(NewGoKitLogger(logger)).Log("msg", "invisible")

	assert.Empty(t, lines())
}

func TestNewGoKitLogger_with_prefixes_fields(t *testing.T) {
	logger, lines := captureLogger(t, logrus.DebugLevel)

	_ = log.With(NewGoKitLogger(logger), "component", "memberlist", "role", "scheduler").Log("msg", "m")

	got := lines()
	require.Len(t, got, 1)
	assert.Equal(t, "memberlist", got[0]["component"])
	assert.Equal(t, "scheduler", got[0]["role"])
}

// A nil logger falls back to the logrus standard logger rather than panicking
// on the first dskit log line.
func TestNewGoKitLogger_nil_falls_back(t *testing.T) {
	require.NotPanics(t, func() {
		_ = NewGoKitLogger(nil).Log("msg", "m")
	})
}

func TestNewGoKitLogger_always_returns_nil(t *testing.T) {
	logger, _ := captureLogger(t, logrus.DebugLevel)
	assert.NoError(t, NewGoKitLogger(logger).Log("msg", "m"))
}
