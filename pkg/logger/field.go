package logger

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field is one structured key/value pair attached to a line with
// [Logger.Debug], [Logger.Info], [Logger.Warn], [Logger.Error] or
// [Logger.With]. Build one with the constructors below - a caller never
// needs go.uber.org/zap itself to use this package.
type Field = zapcore.Field

// String attaches key=value as a string.
func String(key, value string) Field { return zap.String(key, value) }

// Int attaches key=value as an integer.
func Int(key string, value int) Field { return zap.Int(key, value) }

// Int64 attaches key=value as a 64-bit integer.
func Int64(key string, value int64) Field { return zap.Int64(key, value) }

// Float64 attaches key=value as a floating-point number.
func Float64(key string, value float64) Field { return zap.Float64(key, value) }

// Bool attaches key=value as a boolean.
func Bool(key string, value bool) Field { return zap.Bool(key, value) }

// Duration attaches key=value, formatted the way [time.Duration.String]
// does.
func Duration(key string, value time.Duration) Field { return zap.Duration(key, value) }

// Time attaches key=value in RFC 3339 form, matching the "time" member every
// line already carries - see [New].
func Time(key string, value time.Time) Field { return zap.Time(key, value) }

// Err attaches err under the key "err" - not zap's own default, "error" -
// because that is the name dskit and pkg/cluster's go-kit bridge already
// write it under, so a line this package writes about the same failure names
// it the same way.
func Err(err error) Field { return zap.NamedError("err", err) }

// Any attaches key=value for a type none of the above fits. Prefer a typed
// constructor when one exists: Any falls back to reflection to find out how
// to encode value.
func Any(key string, value any) Field { return zap.Any(key, value) }
