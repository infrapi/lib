# infrapi/lib

Opinionated Go building blocks shared by InfraPI services.

Packages: [`api`](docs/packages/api.md), [`auth`](docs/packages/auth.md),
[`client`](docs/packages/client.md), [`config`](docs/packages/config.md),
[`errors`](docs/packages/errors.md), [`listener`](docs/packages/listener.md),
[`logger`](docs/packages/logger.md), [`server`](docs/packages/server.md),
[`tofu`](docs/packages/tofu.md), [`tracing`](docs/packages/tracing.md) and
[`vault`](docs/packages/vault.md). The [package table](docs/index.md#packages) says what
each one does and lists its import path.

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

Continue with [Getting started](docs/getting-started.md), or browse the [documentation](docs/index.md).

## License

MIT, see [LICENSE](https://github.com/infrapi/lib/blob/main/LICENSE).
