# infrapi/lib

Opinionated Go building blocks shared by InfraPI services. Five small packages, no framework:

| Package | Import path | What it does |
|---------|-------------|--------------|
| [`config`](https://infrapi.github.io/lib/packages/config/) | `github.com/infrapi/lib/pkg/config` | Reads a dotenv file, validates values, exposes a typed `AppConfig`. |
| [`errors`](https://infrapi.github.io/lib/packages/errors/) | `github.com/infrapi/lib/pkg/errors` | RFC 9457 application error with an HTTP status, a cause and a remediation hint. |
| [`http`](https://infrapi.github.io/lib/packages/http/) | `github.com/infrapi/lib/pkg/http` | Resty-based API client with TLS/mTLS, retry and hedging. |
| [`listener`](https://infrapi.github.io/lib/packages/listener/) | `github.com/infrapi/lib/pkg/listener` | Opens the service socket, TCP or systemd socket activation. |
| [`server`](https://infrapi.github.io/lib/packages/server/) | `github.com/infrapi/lib/pkg/server` | `gin.Engine` pre-wired with CORS, trusted proxies and a metadata endpoint. |

## Install

```bash
go get github.com/infrapi/lib
```

Requires Go 1.25 or later.

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
