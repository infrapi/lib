# infrapi/lib

Opinionated Go building blocks shared by InfraPI services. The framework that
assembles them into a service — a contract, its handlers, and everything else in
one call — is [infrapi/app](https://github.com/infrapi/app).

| Package | Import path | What it does |
|---------|-------------|--------------|
| [`cluster`](https://infrapi.github.io/lib/packages/cluster/) | `github.com/infrapi/lib/pkg/cluster` | Runs a service as coordinated replicas: a ring per role, leader or broadcast. Its own module. |
| [`config`](https://infrapi.github.io/lib/packages/config/) | `github.com/infrapi/lib/pkg/config` | Reads a dotenv file, validates values, exposes a typed `AppConfig`. |
| [`contract`](https://infrapi.github.io/lib/packages/contract/) | `github.com/infrapi/lib/pkg/contract` | Writes an OpenAPI contract from a short description of a service's resources, fields and operations. |
| [`errors`](https://infrapi.github.io/lib/packages/errors/) | `github.com/infrapi/lib/pkg/errors` | RFC 9457 application error with an HTTP status, a cause, a remediation hint, a stable code and an explicit retryable flag. |
| [`gateway`](https://infrapi.github.io/lib/packages/gateway/) | `github.com/infrapi/lib/pkg/gateway` | Typed CRUD client for the Apache APISIX Admin API: routes, services, upstreams, consumers. |
| [`gatewaysync`](https://infrapi.github.io/lib/packages/gatewaysync/) | `github.com/infrapi/lib/pkg/gatewaysync` | Keeps APISIX routing to a clustered service's replicas in line with its ring and leader. |
| [`http`](https://infrapi.github.io/lib/packages/http/) | `github.com/infrapi/lib/pkg/http` | Resty-based API client with TLS/mTLS, retry and hedging. |
| [`idempotency`](https://infrapi.github.io/lib/packages/idempotency/) | `github.com/infrapi/lib/pkg/idempotency` | Makes a write safe to repeat: an `Idempotency-Key` store and gin middleware, on Postgres or in memory. |
| [`listener`](https://infrapi.github.io/lib/packages/listener/) | `github.com/infrapi/lib/pkg/listener` | Opens the service socket, TCP or systemd socket activation. |
| [`server`](https://infrapi.github.io/lib/packages/server/) | `github.com/infrapi/lib/pkg/server` | `gin.Engine` pre-wired with CORS, trusted proxies, Prometheus metrics, generated OpenAPI docs, and rendering an `errors.Error` as `application/problem+json`. |
| [`tofu`](https://infrapi.github.io/lib/packages/tofu/) | `github.com/infrapi/lib/pkg/tofu` | Drives any OpenTofu provider plugin and resource type, and applies a declarative set of providers and resources on top. |

## Install

```bash
go get github.com/infrapi/lib
```

`cluster` is a module of its own, so that a service which does not cluster does
not inherit the dependency tree of [dskit](https://github.com/grafana/dskit):

```bash
go get github.com/infrapi/lib/pkg/cluster
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
