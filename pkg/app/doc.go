// Package app runs an InfraPI service: one call wires everything an API
// shares, and the service brings only what is its own — its contracts and
// their handlers.
//
//	func main() {
//		var cfg Config // the service's own section of the file
//		app.Main(app.Spec{
//			Name:         "orders-api",
//			Service:      &cfg,
//			LeaderWrites: true,
//			API: func(ctx context.Context, rt *app.Runtime) ([]app.Version, error) {
//				h := newHandlers(cfg)
//				return []app.Version{{
//					OpenAPI: server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
//					Routes:  func(r app.Router) { r.POST("/orders/v1/orders", h.create) },
//				}}, nil
//			},
//		})
//	}
//
// What it wires, all from one configuration — a YAML file whose sections are
// the library's own keys, overridable from the environment:
//
//   - the engine of [server.NewServerGin]: JSON access logs, metrics per
//     route, /-/metrics, /-/metadata, and the contracts at /-/docs;
//   - the socket of [listener.Listen]: TCP or systemd socket activation;
//   - on every version's routes, in order: the path parameters into the
//     access log, the API key, the relay of writes to the leader, validation
//     against that version's contract, Idempotency-Key;
//   - errors as application/problem+json with a code and an explicit
//     retryable flag ([problem]);
//   - a cluster ([cluster]) whose leader alone performs writes, and keeps an
//     APISIX gateway's upstreams and routes in line with the ring;
//   - /-/healthz, /-/readyz, a landing page, the cluster's status pages, and a
//     shutdown that hands leadership over before it stops serving.
package app
