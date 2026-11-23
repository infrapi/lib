---
title: events
description: A service's events, to Kayra's events.infrapi.io - the recorder that queues, batches, merges the same event into a series and limits spam per object, as Kubernetes' EventRecorder.
weight: 42
---

# events

```go
import "github.com/infrapi/lib/pkg/events"
```

Reports what happened to the service's resources to Kayra's
[events.infrapi.io](https://infrapi.github.io/kayra/architecture/events/):
a VM created, a reboot that failed. `Recorder` does what Kubernetes'
`EventRecorder` does. It never blocks the caller and never fails it, because
events are best effort.

- [`server.WithEvents`](server.md#events) records one for every write the
  service serves.
- `rec.Event` records what a worker does.

## The recorder

```go
tokens, err := auth.NewClientCredentials(auth.ClientCredentialsOptions{
	Kayra: kayra, ClientID: identity.Subject("compute.infrapi.io"), Key: keys[0]})
rec := events.NewRecorder(events.Options{
	Controller: "compute",
	Sender:     events.Kayra(cfg.AuthURL, tokens.Token, nil),
})
defer rec.Close(ctx) // sends what is queued, and every series' count

rec.Event(events.Regarding{API: "compute.infrapi.io", Namespace: "core", Resource: "vms", Name: "web-1"},
	events.Normal, "VMRebooted", "rebooted on host h-12")
```

- **`events.Kayra`:** sends the events to Kayra's events group, found in its
  `/.well-known/kayra.json`. It authenticates with the API's
  [own identity](identity.md): Kayra lets an API report about its own objects
  only, in the namespaces subscribed to it.
- **Without a `Sender`,** or while Kayra cannot be reached, the events go to
  the log (`events.Log`).
- **`Emit(e)`** records a whole `events.Event`: `Action`, `User`, `RequestID`,
  `Details`.

## Series

The same event, its object, type, reason, controller and note, seen again
within `SeriesWindow` (10 minutes) is not sent again:

- the first is sent at once;
- the next ones only count;
- the count is sent every `SeriesFlush` (30 seconds) while it changes, and
  once more at `Close`.

Thirty identical failures are one event, its `series.count` 30.

## Spam

Each object gets `SpamBurst` events (25), then one every `SpamRefill`
(5 minutes), Kubernetes' values. Over the limit, an event already in a series
is counted in it, and a new one is dropped.

## Sending

- **Batches:** up to `Batch` events (100), or every `BatchWait` (1 second), a
  request per namespace.
- **The queue:** `Queue` events (1000). A full queue drops the event.
- **A `429`:** the recorder waits as `Retry-After` asks, a minute at most, then
  tries once more.
- **Metrics:** `infrapi_events_total{result}`: `sent`, `aggregated`, `dropped`
  or `failed`, at the service's `-/metrics`.

## Configuration

[`config.GetEvents`](config.md#application-configuration) reads
`INFRAPI_EVENTS_ENABLED` (false: the log only), `INFRAPI_EVENTS_QUEUE`,
`INFRAPI_EVENTS_BATCH`, `INFRAPI_EVENTS_SERIES_WINDOW`,
`INFRAPI_EVENTS_SPAM_BURST` and `INFRAPI_EVENTS_SPAM_REFILL`. A project of
[`infrapi-cli app init`](https://infrapi.github.io/cli/getting-started/#1-install)
builds its recorder from them.
