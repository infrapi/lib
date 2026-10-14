# infrapi/lib

Opinionated Go building blocks shared by InfraPI services.

| Package | Import path | What it does |
|---------|-------------|--------------|
| [`config`](https://infrapi.github.io/lib/packages/config/) | `github.com/infrapi/lib/pkg/config` | Reads a dotenv file, validates values, exposes a typed `AppConfig`. |
| [`errors`](https://infrapi.github.io/lib/packages/errors/) | `github.com/infrapi/lib/pkg/errors` | RFC 9457 application error with an HTTP status, a cause, a remediation hint, a stable code and an explicit retryable flag. |
| [`http`](https://infrapi.github.io/lib/packages/http/) | `github.com/infrapi/lib/pkg/http` | Resty-based API client with TLS/mTLS, retry and hedging. |
| [`listener`](https://infrapi.github.io/lib/packages/listener/) | `github.com/infrapi/lib/pkg/listener` | Opens the service socket, TCP or systemd socket activation. |
| [`server`](https://infrapi.github.io/lib/packages/server/) | `github.com/infrapi/lib/pkg/server` | `gin.Engine` pre-wired with CORS, trusted proxies, Prometheus metrics, generated OpenAPI docs, and rendering an `errors.Error` as `application/problem+json`. |
| [`tofu`](https://infrapi.github.io/lib/packages/tofu/) | `github.com/infrapi/lib/pkg/tofu` | Drives any OpenTofu provider plugin and resource type, and applies a declarative set of providers and resources on top. |

## Install

```bash
go get github.com/infrapi/lib
```

Requires Go 1.26.7 or later.

## In one minute

```go
package main

import (
	"log"

	"github.com/infrapi/lib/pkg/config"
	"github.com/infrapi/lib/pkg/server"
)

func main() {
	vars, err := config.NewConfig()
	if err != nil {
		log.Fatal(err)
	}

	cfg, err := vars.GetAppConfig()
	if err != nil {
		log.Fatal(err)
	}

	engine, err := server.NewServerGin(cfg)
	if err != nil {
		log.Fatal(err)
	}

	if err := engine.Run(cfg.AppListenAddress); err != nil {
		log.Fatal(err)
	}
}
```

Continue with [Getting started](https://infrapi.github.io/lib/getting-started/).

## License

MIT, see [LICENSE](https://github.com/infrapi/lib/blob/main/LICENSE).
