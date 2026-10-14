# listener

```go
import "github.com/infrapi/lib/pkg/listener"
```

Opens the socket a service accepts connections on, from the same `AppConfig` the
[server](server.md) package consumes.

```go
ln, err := listener.Listen(cfg)
if err != nil {
	return err
}
```

`cfg.AppListenType` decides what happens:

| Type | Behaviour |
|------|-----------|
| `tcp` | Binds `cfg.AppListenAddress`, the default `127.0.0.1:8080`. |
| `systemd` | Adopts the socket systemd passed through socket activation. |
| anything else | Error. The dotenv validator already rejects it at load time. |

## With the server package

`Listen` returns a plain `net.Listener` and `NewServerGin` returns a plain
`http.Handler`, so the two meet at `http.Server`:

```go
ln, err := listener.Listen(cfg)
if err != nil {
	return err
}

engine, err := server.NewServerGin(cfg)
if err != nil {
	return err
}

srv := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
	return err
}
```

Nothing about the service changes between a TCP socket and an activated one: the
same binary serves whichever `Listen` returns.

## Socket activation

Under systemd the socket is opened by the unit and handed to the process on file
descriptor 3, announced through two environment variables:

- `LISTEN_PID` must equal the PID of the process, otherwise the descriptor
  belongs to someone else and `Listen` refuses it.
- `LISTEN_FDS` is the number of descriptors passed; `0` means systemd has a
  problem, and only the first descriptor is used.

The descriptor is adopted with `net.FileListener`, which duplicates it, so the
one systemd passed is closed once the listener exists. No systemd library is
pulled in for this.

A matching unit pair looks like:

```ini title="myservice.socket"
[Socket]
ListenStream=127.0.0.1:8080

[Install]
WantedBy=sockets.target
```

```ini title="myservice.service"
[Service]
ExecStart=/usr/bin/myservice
Environment=INFRAPI_APP_LISTEN_TYPE=systemd
```

`examples/listener` runs the TCP path, serves the gin engine on it, and shows
both error paths.
