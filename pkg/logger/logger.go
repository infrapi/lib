// Package logger builds the [Logger] every InfraPI package logs structured
// lines through: cluster, server, app, a service built on them - one shared
// set of conventions instead of each package picking its own.
//
// It is built on uber-go/zap, but that is an implementation detail: nothing
// in this package's API takes or returns a zap type, so a caller never adds
// go.uber.org/zap to its own imports. Attach fields with [String], [Int],
// [Err] and friends below.
//
// Lines keep the field names the rest of the system already writes - time,
// level, msg - and the same level names logrus and the pkg/app slog setup
// use, warning included, so a collector parses a line from this package the
// same way it parses the engine's access log or a logrus line. See the
// unexported parseLevel for the level names [Options.Level] accepts.
//
//	l, err := logger.New(logger.Options{
//		Level:  appCfg.AppLogLevel.String(),
//		Format: appCfg.AppLogFormat,
//		Name:   "cluster",
//	})
//	if err != nil {
//		return err
//	}
//	defer func() { _ = l.Sync() }()
//
//	logger.SetDefault(l)
//	logger.L().Info("replica joined", logger.String("role", "scheduler"))
//
// [SetDefault] is how one logger reaches every package that calls [L]
// afterwards, without threading a *Logger through every constructor. Called
// once, early in main, after Options is built from INFRAPI_APP_LOG_LEVEL and
// INFRAPI_APP_LOG_FORMAT. Before that call, L answers with a logger built
// from Options{}'s own defaults - info level, console format, os.Stdout -
// never nil.
package logger

import (
	"errors"
	"io"
	"os"
	"sync"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// The two formats a line can be written in.
const (
	// FormatJSON is one JSON object per line: time, level, msg, plus whatever
	// fields the call site added. This is the schema pkg/server's access log
	// and pkg/app's slog setup already write with INFRAPI_APP_LOG_FORMAT=json,
	// so a collector reads a line from this package, a logrus line and the
	// engine's own access log the same way.
	FormatJSON = "json"

	// FormatConsole writes a human-readable line: level, timestamp and
	// message first, fields after. It is what Options{} and an unrecognised
	// Format both fall back to - the same default INFRAPI_APP_LOG_FORMAT="text"
	// carries elsewhere.
	FormatConsole = "console"
)

// Options configures [New]. The zero value is a safe default: info level,
// console format, written to os.Stdout - the same defaults
// INFRAPI_APP_LOG_LEVEL and INFRAPI_APP_LOG_FORMAT carry.
type Options struct {
	// Level is the lowest level a line must reach to be written: trace,
	// debug, info, warn (or warning), error. Empty means info.
	Level string

	// Format is [FormatJSON] for one JSON object per line. Anything else,
	// including empty, writes [FormatConsole].
	Format string

	// Name tags every line this logger writes with a "component" field, e.g.
	// "cluster" or "server", so lines from several packages in one process
	// are told apart once they reach a collector. Empty adds nothing.
	Name string

	// Output is where lines are written. Nil means os.Stdout, the same
	// destination pkg/server's access log and pkg/app's slog setup use.
	Output io.Writer

	// Development adds the call site and a stacktrace to error-and-above
	// lines. Off by default: a stacktrace is several lines competing with the
	// one-line-per-record shape a collector expects, and pkg/server already
	// puts a recovered panic's stack in a single field of its own when that
	// is what is needed.
	Development bool
}

// Logger is the handle every InfraPI package logs through. The zero value is
// not usable - build one with [New] - but every *Logger New returns is safe
// for concurrent use, the same guarantee zap itself makes.
type Logger struct {
	core  *zap.Logger
	sugar *zap.SugaredLogger
}

// wrap is the only place a *zap.Logger becomes a *Logger, so Sync's ignorable
// error handling and the sugar cache are never built twice from the same
// core.
func wrap(z *zap.Logger) *Logger {
	return &Logger{core: z, sugar: z.Sugar()}
}

// New builds a Logger from opts. The only error it returns is an unknown
// Level; a recognised Level, any Format and any Output never fail.
func New(opts Options) (*Logger, error) {
	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}

	output := opts.Output
	if output == nil {
		output = os.Stdout
	}

	encoder := zapcore.NewConsoleEncoder(encoderConfig())
	if opts.Format == FormatJSON {
		encoder = zapcore.NewJSONEncoder(encoderConfig())
	}

	// Lock: cluster, server and app all log from several goroutines of their
	// own, onto what is usually one shared *os.File.
	core := zapcore.NewCore(encoder, zapcore.Lock(zapcore.AddSync(output)), level)

	var zopts []zap.Option
	if opts.Development {
		zopts = append(zopts, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	}

	z := zap.New(core, zopts...)
	if opts.Name != "" {
		z = z.Named(opts.Name)
	}
	return wrap(z), nil
}

// Debug writes msg at debug level, with fields attached.
func (l *Logger) Debug(msg string, fields ...Field) { l.core.Debug(msg, fields...) }

// Info writes msg at info level, with fields attached.
func (l *Logger) Info(msg string, fields ...Field) { l.core.Info(msg, fields...) }

// Warn writes msg at warn level, with fields attached. It is encoded as
// "warning" - see [New] - not "warn".
func (l *Logger) Warn(msg string, fields ...Field) { l.core.Warn(msg, fields...) }

// Error writes msg at error level, with fields attached.
func (l *Logger) Error(msg string, fields ...Field) { l.core.Error(msg, fields...) }

// Fatal writes msg at error level, with fields attached, then calls
// os.Exit(1). Reserve it for a startup failure nothing can recover from - a
// request handler should return an error instead, the way every other
// package in this module already does.
func (l *Logger) Fatal(msg string, fields ...Field) { l.core.Fatal(msg, fields...) }

// Debugf writes a printf-formatted message at debug level, for a call site
// that wants that over typed fields.
func (l *Logger) Debugf(format string, args ...any) { l.sugar.Debugf(format, args...) }

// Infof writes a printf-formatted message at info level.
func (l *Logger) Infof(format string, args ...any) { l.sugar.Infof(format, args...) }

// Warnf writes a printf-formatted message at warn level.
func (l *Logger) Warnf(format string, args ...any) { l.sugar.Warnf(format, args...) }

// Errorf writes a printf-formatted message at error level.
func (l *Logger) Errorf(format string, args ...any) { l.sugar.Errorf(format, args...) }

// With returns a Logger that attaches fields to every line it writes from
// here on, in addition to this one's own. The receiver is unchanged.
func (l *Logger) With(fields ...Field) *Logger {
	return wrap(l.core.With(fields...))
}

// Named returns a Logger whose "component" field is name, appended after any
// this one already carries. The receiver is unchanged.
func (l *Logger) Named(name string) *Logger {
	return wrap(l.core.Named(name))
}

// Sync flushes buffered lines. Call it with defer right after building a
// Logger, before the process exits.
//
// A logger writing to a terminal returns an error here on Linux and Darwin -
// stdout and stderr refuse the sync(2) call zap issues underneath - which is
// not a sign any line was lost. Sync reports that case as success, the same
// way pkg/server already treats a client closing its connection as
// unremarkable rather than an error worth surfacing.
func (l *Logger) Sync() error {
	err := l.core.Sync()
	if err == nil || isIgnorableSyncError(err) {
		return nil
	}
	return err
}

var (
	mu      sync.RWMutex
	current = buildDefault()
)

// buildDefault is Options{}'s own logger. parseLevel("") and the zero Options
// never fail, so this never does either.
func buildDefault() *Logger {
	l, err := New(Options{})
	if err != nil {
		panic(err)
	}
	return l
}

// SetDefault replaces the logger [L] returns. Call it once, early in main,
// once Options is built from the service's own configuration; every package
// that calls L afterwards - cluster and server included, since neither holds
// a logger of its own before this call - picks up the replacement.
func SetDefault(l *Logger) {
	if l == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	current = l
}

// L returns the current default logger: the one passed to the last
// [SetDefault] call, or Options{}'s own logger before the first one. Never
// nil.
func L() *Logger {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Sync flushes the current default logger's buffered lines. Call it with
// defer right after [SetDefault], before the process exits.
func Sync() error {
	return L().Sync()
}

func isIgnorableSyncError(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EBADF)
}
