# errors

```go
import infraerrors "github.com/infrapi/lib/pkg/errors"
```

One error type for everything a service hands back to an API client. Its JSON
form is an [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem detail,
served as `application/problem+json`.

```go
type Error struct {
	Type     string `json:"type,omitempty"`
	Code     int    `json:"status"`
	Title    string `json:"title"`
	Message  string `json:"detail"`
	Instance string `json:"instance,omitempty"`
	Origin   string `json:"origin,omitempty"`
	Solution string `json:"solution,omitempty"`

	Err error `json:"-"`
}
```

| Member | Meaning |
|--------|---------|
| `type` | URI identifying the problem type. Absent means `about:blank`: nothing beyond the status code. |
| `status` | The `net/http` status code. The response carries the same one. |
| `title` | Short summary of the problem *type*, identical on every occurrence. Left empty it is serialised as the status phrase, which is what `about:blank` requires. |
| `detail` | What happened this time, written for the client. |
| `instance` | URI identifying this occurrence: a request path, or an opaque id. |
| `origin` | Extension member: the failing call path, `MyPackage.MyFunc.MyCall`. For your operators. |
| `solution` | Extension member: how to fix it. |

`Err` is the wrapped cause. It is never serialised.

## Raising an error

The second argument is the origin, not the title:

```go
return infraerrors.New(http.StatusNotImplemented, "subscription.SetState",
	"state %q not supported", state)
```

That is a complete problem detail already: no `type`, so `about:blank`, and the
title comes out as `Not Implemented`. Add the remediation when you know it:

```go
return infraerrors.New(http.StatusNotFound, "user.Get", "no user with id %d", id).
	WithSolution("list the users first")
```

A problem clients should be able to branch on deserves a type URI, with a title
describing that type rather than this occurrence:

```go
return infraerrors.New(http.StatusConflict, "subscription.Create", "id %q is taken", id).
	WithType("https://infrapi.example.com/errors/duplicate-subscription", "Duplicate subscription")
```

## Wrapping a cause

`Wrap` keeps the original error reachable, so `errors.Is` and `errors.As` still
work several layers up:

```go
cfg, err := config.NewConfig()
if err != nil {
	return infraerrors.Wrap(err, http.StatusBadGateway, "config.Read", "")
}
```

An empty format takes the message of the cause as the detail. Pass one to say
something more useful, or when the cause holds internals:

```go
return infraerrors.Wrap(err, http.StatusBadGateway, "config.Read",
	"unable to read %s", path)
```

!!! warning "Do not flatten the cause"
    `New(code, origin, err.Error())` compiles and reads fine, but it turns the
    cause into a string: every `errors.Is` above that point stops matching. Use
    `Wrap`.

The printed form carries every member that is set, which is more than the
payload shows:

```text
infrapi-error: status=502 | title=Bad Gateway | origin=config.Read | detail=unable to read app.env | cause=open app.env: no such file or directory
```

`cause=` is left out when it is already the detail.

## In a handler

`ErrorCode` picks the status of any error, whoever produced it:

| Argument | Result |
|----------|--------|
| `nil` | `0` |
| an `*Error`, however deeply wrapped | its `Code` |
| any other error | `500`, since nobody identified it |

`ErrorCast` gives the payload to serialise, synthesising an untyped `500` for a
foreign error while keeping its chain:

```go
func handle(c *gin.Context) {
	if err := doTheWork(); err != nil {
		payload, _ := json.Marshal(infraerrors.ErrorCast(err))
		c.Data(infraerrors.ErrorCode(err), infraerrors.MediaType, payload)
		return
	}
	c.JSON(http.StatusOK, result)
}
```

`c.Data` rather than `c.JSON`, because the payload is served as
`infraerrors.MediaType`:

```json
{
  "type": "https://infrapi.example.com/errors/unknown-user",
  "status": 404,
  "title": "Unknown user",
  "detail": "no user with id 42",
  "instance": "/users/42",
  "origin": "user.Get",
  "solution": "list the users first"
}
```

!!! note "Nil in, nil out"
    `ErrorCast(nil)` returns a nil `*Error`. Never assign it straight to a
    variable of type `error`: a nil pointer in an error interface is not nil.

!!! warning "What reaches the client"
    `Err` is `json:"-"`, so the cause is never serialised. Its *text* still can
    be: `Wrap` with an empty format copies `err.Error()` into `detail`, and
    `ErrorCast` does the same for a foreign error. When the cause may carry a
    path, a DSN or a hostname, pass an explicit format and let the real message
    live in your logs.

`examples/errors` runs all of it, payloads included.
