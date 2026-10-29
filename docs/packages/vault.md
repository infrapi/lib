# vault

```go
import infravault "github.com/infrapi/lib/pkg/vault"
```

Wraps [`github.com/hashicorp/vault-client-go`](https://pkg.go.dev/github.com/hashicorp/vault-client-go)
with InfraPI defaults - TLS/mTLS, retry - and an opinionated surface for the KV v2 and
PKI secrets engines. A use case that does not fit it reaches for
[`Client.APIClient`](#escape-hatch) instead, the vanilla Vault client.

## Creating a client

```go
client, err := infravault.NewClient(infravault.ClientOptions{
	Address: "https://vault.example.com:8200",
})
```

`Address` is mandatory; everything else has a default:

| Option | Type | Default |
|--------|------|---------|
| `Namespace` | `string` | none (Vault Enterprise only) |
| `Timeout` | `time.Duration` | `DefaultTimeout` (60s) |
| `RetryCount` | `int` | `DefaultRetryCount` (2); negative for no retry |
| `RetryWaitTime` | `time.Duration` | `DefaultRetryWaitTime` (1s) |
| `RetryMaxWaitTime` | `time.Duration` | `DefaultRetryMaxWaitTime` (1.5s) |

These match what `vault-client-go` itself documents as its own defaults, made explicit
here rather than left to the dependency to pick.

## TLS and mTLS

Certificates can be passed as PEM bytes or as filesystem paths; when both are given, the
in-memory content wins:

```go
client, err := infravault.NewClient(infravault.ClientOptions{
	Address: "https://vault.example.com:8200",

	CACertPath: "/etc/ssl/certs/internal-ca.pem",

	ClientCertPath: "/etc/ssl/certs/myapp.pem",
	ClientKeyPath:  "/etc/ssl/private/myapp.key",
})
```

| Option | Purpose |
|--------|---------|
| `CACertContent` / `CACertPath` | Trust an internal CA for the Vault server certificate. |
| `ClientCertContent` / `ClientKeyContent` | mTLS keypair from memory, for the cert auth method. |
| `ClientCertPath` / `ClientKeyPath` | mTLS keypair from disk. |

## Authenticating

```go
err := client.Login(ctx, infravault.LoginOptions{
	AppRole: &infravault.AppRoleLogin{RoleID: roleID, SecretID: secretID},
})
```

`LoginOptions` is one of:

- `AppRole`: authenticates via the [AppRole auth method](https://developer.hashicorp.com/vault/docs/auth/approle).
  `RoleID` is mandatory, and `SecretID` or `SecretIDFile`, a file holding it, read at each
  login so that a rotated SecretID is picked up; `MountPath` defaults to
  `DefaultAppRoleMountPath` (`"approle"`).
- `Token`: sets the client's token directly, for one already issued out of band (`vault
  login`, a token minted by another service), without a login call of its own.

A successful call leaves every subsequent request on `client` carrying the resulting
token - there is nothing further to pass around.

With `AppRole`, the client keeps the token valid on its own, for a long-running service:
it logs in again shortly before the token's lease ends, and once more when Vault refuses a
call with a 403 (a token revoked, or expired before its time), then retries that call.
Calls refused at once log in again once. A `Token` is used as it is: Vault refusing it is
the caller's error.

## Errors

Every call returns an [`infraerrors.Error`](errors.md), the vault-client-go error as its
cause (vault-client-go's `IsErrorStatus` still works on it):

| Vault's answer | Error |
|---|---|
| 404 | `NotFound` |
| 401, 403 | `UpstreamAuth`: Vault refused the client's token or its AppRole login |
| 5xx | `Upstream`, retryable |
| another status | `Upstream` |
| none (connection refused, timeout) | `Unavailable`, retryable |

## Secrets (KV v2)

```go
data, err := client.GetSecret(ctx, "secret", "myapp/db")
_, err = client.SetSecret(ctx, "secret", "myapp/db", map[string]any{"password": "new"})
err = client.DeleteSecret(ctx, "secret", "myapp/db")
keys, err := client.ListSecrets(ctx, "secret", "myapp/")
```

Every call's second argument is the KV v2 engine's mount path (`"secret"` above), the
third the path under it. `SetSecret` returns what Vault now stores at that path - KV v2's
own write response carries only version metadata (created time, version number), never
the data just written, so `SetSecret` reads it back for you.

## Certificates (PKI)

```go
cert, err := client.GetCertificate(ctx, "pki", serial)
```

Returns the PEM-encoded certificate for serial from the PKI engine mounted at `"pki"`.

## Escape hatch

`client.APIClient` is the underlying `*vault.Client` from `vault-client-go`. Anything
neither wrapper covers - other secrets engines, system endpoints, a different auth
method - is reachable there, and every call above takes a `context.Context` so a
per-call deadline or cancellation works the same way it would calling `APIClient`
directly.
