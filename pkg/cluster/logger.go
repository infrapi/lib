package cluster

import (
	"fmt"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/sirupsen/logrus"
)

// missingValue is what go-kit itself appends when a key arrives without a
// value, so a caller grepping for it finds the same token either side of the
// adapter.
const missingValue = "(MISSING)"

// messageKey is the key dskit puts the human-readable part of a log line under.
const messageKey = "msg"

// NewGoKitLogger adapts a logrus logger to the go-kit interface every dskit
// component expects.
//
// The "level" pair selects the logrus level and an unlevelled line is logged at
// info, because dskit and hashicorp/memberlist both emit some. The "msg" pair
// becomes the logrus message. Every other pair becomes a logrus field, "err"
// included: dskit names it that way everywhere, and renaming it would break
// queries written against the component's own documentation.
//
// Filtering is left to logrus, which already drops anything below the level
// AppConfig asked for.
func NewGoKitLogger(l logrus.FieldLogger) log.Logger {
	if l == nil {
		l = logrus.StandardLogger()
	}
	return &goKitLogger{logger: l}
}

// goKitLogger is stateless, so it is safe for the concurrent use dskit makes of
// it from its own goroutines.
type goKitLogger struct {
	logger logrus.FieldLogger
}

// Log implements log.Logger. It always returns nil: dskit sometimes checks the
// error returned by level.Error(...).Log(...) and a non-nil value there would
// be noise rather than information.
func (g *goKitLogger) Log(keyvals ...any) error {
	fields := make(logrus.Fields, len(keyvals)/2)
	message := ""
	lvl := logrus.InfoLevel
	seenMessage := 0

	for i := 0; i < len(keyvals); i += 2 {
		key := fmt.Sprint(keyvals[i])

		var value any = missingValue
		if i+1 < len(keyvals) {
			value = keyvals[i+1]
		}

		switch key {
		case level.Key():
			lvl = parseLevel(value)
		case messageKey:
			// log.With can stack several messages. The last one is the line
			// being logged; the earlier ones are context and are kept as
			// fields rather than dropped.
			if message != "" {
				seenMessage++
				fields[fmt.Sprintf("%s_%d", messageKey, seenMessage)] = message
			}
			message = fmt.Sprint(value)
		default:
			fields[key] = value
		}
	}

	entry := g.logger.WithFields(fields)

	switch lvl {
	case logrus.ErrorLevel:
		entry.Error(message)
	case logrus.WarnLevel:
		entry.Warn(message)
	case logrus.DebugLevel:
		entry.Debug(message)
	default:
		entry.Info(message)
	}

	return nil
}

// parseLevel maps a go-kit level value onto a logrus level. go-kit's own values
// implement Stringer, and so does anything else worth reading here; an
// unrecognised value logs at info rather than disappearing.
func parseLevel(value any) logrus.Level {
	name := ""
	switch v := value.(type) {
	case string:
		name = v
	case fmt.Stringer:
		name = v.String()
	default:
		name = fmt.Sprint(value)
	}

	switch name {
	case "error":
		return logrus.ErrorLevel
	case "warn":
		return logrus.WarnLevel
	case "debug":
		return logrus.DebugLevel
	default:
		return logrus.InfoLevel
	}
}
