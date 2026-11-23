---
title: identity
description: A service's own identity at Kayra, system:api:<group> - its EC P-256 key, the public half served at -/jwks.json, the assertions it signs.
weight: 45
---

# identity

```go
import "github.com/infrapi/lib/pkg/identity"
```

A registered API can have an identity of its own at Kayra,
`system:api:<group>`: the app behind it gets tokens as the API, to call
Kayra or another API without a user. The app proves it is the API with a key
of its own:

- it signs its assertions with the private key (ES256);
- it serves the public half at `<root>/-/jwks.json`
  ([`server.WithIdentity`](server.md#identity-keys)), where Kayra's
  reconciler reads it when the registration says `identity.jwks_from:
  spec`.

[`auth.ClientCredentials`](auth.md#the-apis-own-tokens) gets the tokens. See
Kayra's [API identity](https://infrapi.github.io/kayra/architecture/api-identity/)
for the registration and the rights.

## The key

Keys are EC P-256, in PEM: PKCS#8 (`PRIVATE KEY`) or SEC 1 (`EC PRIVATE
KEY`), as [`infrapi-cli app keygen`](https://infrapi.github.io/cli/getting-started/#1-install)
and `openssl ecparam -name prime256v1 -genkey` write them.

```go
keys, err := identity.LoadKeys(cfg.Identity.KeyPaths) // INFRAPI_IDENTITY_KEY_PATH
key, err := identity.LoadKey("/etc/compute/identity.pem")
key, err := identity.GenerateKey()
pem, err := key.PEM()   // PKCS#8
key.KeyID()             // its RFC 7638 thumbprint, the kid
```

`LoadKeys` reads them in order: the first signs, and every one is
published. To rotate a key, serve the new one after the old, register
the API again, then serve the new one alone and register again.

## Its public half

```go
set := identity.PublicJWKS(keys...)
// {"keys":[{"kty":"EC","crv":"P-256","x":"…","y":"…","kid":"…","use":"sig","alg":"ES256"}]}
```

## Signing

```go
jwt, err := key.Sign(map[string]any{"iss": sub, "sub": sub, "aud": tokenEndpoint, "exp": exp, "jti": jti})
```

`Sign` is an ES256 JWT, the key's `kid` in its header. `auth.ClientCredentials`
signs its assertions with it.

```go
identity.Subject("compute.infrapi.io") // "system:api:compute.infrapi.io", the client id
```
