package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

var errFake = errors.New("fake")

func TestParseLevel(t *testing.T) {
	cases := map[string]zapcore.Level{
		"":        zapcore.InfoLevel,
		"info":    zapcore.InfoLevel,
		"trace":   zapcore.DebugLevel,
		"debug":   zapcore.DebugLevel,
		"warn":    zapcore.WarnLevel,
		"warning": zapcore.WarnLevel,
		"error":   zapcore.ErrorLevel,
	}
	for name, want := range cases {
		got, err := parseLevel(name)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestParseLevel_unknown(t *testing.T) {
	_, err := parseLevel("panic")
	assert.ErrorContains(t, err, `unknown level "panic"`)
}

func TestNew_unknownLevel(t *testing.T) {
	_, err := New(Options{Level: "nope"})
	assert.Error(t, err)
}

func TestNew_jsonFields(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Format: FormatJSON, Output: &buf, Name: "cluster"})
	require.NoError(t, err)

	l.Info("replica joined", String("role", "scheduler"))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "info", line["level"])
	assert.Equal(t, "replica joined", line["msg"])
	assert.Equal(t, "scheduler", line["role"])
	assert.Equal(t, "cluster", line["component"])
	assert.NotEmpty(t, line["time"])
}

func TestNew_warnLevelIsWarning(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Format: FormatJSON, Output: &buf})
	require.NoError(t, err)

	l.Warn("disk almost full")

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "warning", line["level"])
}

func TestNew_levelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Level: "error", Output: &buf})
	require.NoError(t, err)

	l.Info("dropped")
	l.Warn("dropped too")
	assert.Empty(t, buf.String())

	l.Error("kept")
	assert.Contains(t, buf.String(), "kept")
}

func TestNew_consoleIsDefaultFormat(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Output: &buf})
	require.NoError(t, err)

	l.Info("hello")

	line := buf.String()
	assert.False(t, strings.HasPrefix(line, "{"), "expected console encoding, got %q", line)
	assert.Contains(t, line, "hello")
}

func TestLogger_printfStyle(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Format: FormatJSON, Output: &buf})
	require.NoError(t, err)

	l.Infof("widget %s created", "w-1")

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "widget w-1 created", line["msg"])
}

func TestLogger_withAndNamed(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Format: FormatJSON, Output: &buf})
	require.NoError(t, err)

	l.With(String("request_id", "abc")).Named("server").Info("handled")

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "abc", line["request_id"])
	assert.Equal(t, "server", line["component"])
}

func TestFields_err(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Format: FormatJSON, Output: &buf})
	require.NoError(t, err)

	l.Error("call failed", Err(errFake))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "fake", line["err"])
}

func TestSetDefault_L(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Format: FormatJSON, Output: &buf})
	require.NoError(t, err)

	SetDefault(l)
	t.Cleanup(func() { SetDefault(buildDefault()) })

	L().Info("via L")
	L().Infof("via %s", "Infof")

	assert.Contains(t, buf.String(), "via L")
	assert.Contains(t, buf.String(), "via Infof")
}

func TestSetDefault_nilIgnored(t *testing.T) {
	before := L()
	SetDefault(nil)
	assert.Same(t, before, L())
}

func TestL_neverNil(t *testing.T) {
	assert.NotNil(t, L())
}

func TestSync_bufferIsNoop(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Output: &buf})
	require.NoError(t, err)

	SetDefault(l)
	t.Cleanup(func() { SetDefault(buildDefault()) })

	assert.NoError(t, Sync())
}

func TestIsIgnorableSyncError(t *testing.T) {
	assert.True(t, isIgnorableSyncError(syscall.ENOTTY))
	assert.False(t, isIgnorableSyncError(errFake))
}
