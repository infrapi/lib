# logger

```go
import "github.com/infrapi/lib/pkg/logger"
```

Builds the `*logger.Logger` a service's own code logs through, with one shared
set of conventions so a line from this package reads the same as a line from
anywhere else in the process:

```go
l, err := logger.New(logger.Options{
	Level:  appConfig.AppLogLevel.String(),
	Format: appConfig.AppLogFormat,
	Name:   "cluster",
})
if err != nil {
	return err
}
defer func() { _ = l.Sync() }()

l.Info("replica joined", logger.String("role", "scheduler"))
```

`Level` and `Format` take the same values `INFRAPI_APP_LOG_LEVEL` and
`INFRAPI_APP_LOG_FORMAT` already carry, so a logger for this package is built
from the exact same `*config.AppConfig` the [server](server.md) and
[cluster](cluster.md) packages read.

It is built on [uber-go/zap](https://pkg.go.dev/go.uber.org/zap) internally,
but that is an implementation detail: no function here takes or returns a zap
type, so a caller never adds `go.uber.org/zap` to its own imports. Attach
fields with the constructors this package exports instead.

## Why one more logger

`pkg/app` already wires logrus and `log/slog` together so that every line -
whichever of the two wrote it - carries the same `time`, `level` and `msg`
members, and [pkg/server](server.md)'s JSON access log follows the same
schema. `pkg/logger` extends that convention to zap's typed fields and lower
allocation cost, for a service's own log statements, without breaking the
schema a collector already parses.

It does not replace `pkg/cluster`'s go-kit/logrus bridge - dskit's own logging
stays on logrus - or `pkg/app`'s slog setup. Pick it for a service's
application-level logging; the libraries underneath keep what they already
use.

## Options

| Field | Meaning |
|-------|---------|
| `Level` | `trace`, `debug`, `info`, `warn` (or `warning`), `error`. Empty is `info`. There is no separate trace level underneath, so `trace` filters at the same threshold as `debug`. |
| `Format` | `logger.FormatJSON` for one JSON object per line. Anything else, including empty, writes `logger.FormatConsole`, a human-readable line. |
| `Name` | Tags every line with a `component` field - `"cluster"`, `"server"` - so a collector tells packages apart in one process. Empty adds nothing. |
| `Output` | Where lines are written. Nil means `os.Stdout`. |
| `Development` | Adds the call site and a stacktrace to error-and-above lines. Off by default. |

A JSON line looks like:

```json
{"time":"2026-01-01T00:00:00.000000000Z","level":"info","component":"cluster","msg":"replica joined","role":"scheduler"}
```

`warn` is written as `"warning"`, matching the name logrus and the `slog`
setup in `pkg/app` already use - so filtering a collector on `level="warning"`
finds every warning line in the process, not just the ones logrus wrote.

## Writing a line

Every level takes a message and any number of fields:

```go
l.Info("request handled", logger.String("route", "/widgets"), logger.Int("status", 200))
l.Error("call failed", logger.Err(err))
```

| Constructor | Attaches |
|-------------|----------|
| `logger.String(key, value string)` | A string. |
| `logger.Int(key string, value int)` / `Int64` | An integer. |
| `logger.Float64(key string, value float64)` | A floating-point number. |
| `logger.Bool(key string, value bool)` | A boolean. |
| `logger.Duration(key string, value time.Duration)` | A duration, formatted like `time.Duration.String`. |
| `logger.Time(key string, value time.Time)` | A timestamp, RFC 3339. |
| `logger.Err(err error)` | An error, under the key `"err"` - not zap's own default, `"error"` - matching the name `pkg/cluster`'s go-kit bridge and dskit already write it under. |
| `logger.Any(key string, value any)` | Anything else, found by reflection. |

A call site that wants `fmt.Sprintf`-style logging instead of fields has
`Debugf`, `Infof`, `Warnf` and `Errorf`.

`With` returns a Logger that attaches fields to every line it writes from
there on; `Named` returns one whose `component` field gets name appended.
Both leave the receiver unchanged:

```go
requestLogger := l.With(logger.String("request_id", id)).Named("http")
```

## The package-level default

`SetDefault` is how one logger, built once in `main` after `AppConfig` is
loaded, reaches every package that calls `L` afterwards, without a logger
argument threaded through each package's own constructor:

```go
logger.SetDefault(l)
defer func() { _ = logger.Sync() }()

// anywhere else in the process:
logger.L().Info("handled", logger.Int("status", 200))
```

Before the first `SetDefault` call, `L` answers with `Options{}`'s own logger
- info level, console format, `os.Stdout` - never nil, so a package that logs
before `main` has finished wiring still gets real output instead of a crash.

`Sync` flushes the current default logger's buffered lines. Call it with
`defer` right after `SetDefault`. A logger writing to a terminal returns an
error from the underlying sync call on Linux and Darwin - stdout and stderr
refuse the `sync(2)` call zap issues underneath - which `logger.Sync` treats
as success rather than surfacing it, the same way `pkg/server` already treats
a client closing its connection as unremarkable.

`examples/logger` builds a logger from `AppConfig`, writes JSON, filters by
level, and shows the package-level default.
