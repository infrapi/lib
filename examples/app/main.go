// Command app is the template of an InfraPI service on pkg/app, contract
// first:
//
//   - api/orders/v1.yaml is the contract, written by hand;
//   - go generate turns it into internal/ordersv1: the models, the router,
//     and the interface the handlers implement;
//   - handlers.go implements that interface, one method per operation;
//   - this file wires the two into app.Main.
//
// Everything else — logs, metrics, docs, validation, errors, idempotency,
// cluster, gateway, shutdown — is the library's, configured from config.yaml
// and the environment.
//
//	cd examples/app
//	go run . -config config.yaml
//	curl -s -X POST localhost:8080/orders/v1/tenants/acme/orders -d '{"item":"book"}'
//	open http://localhost:8080/-/docs
//
// Two replicas, every write performed on both (node2 on 127.0.0.2; on macOS,
// sudo ifconfig lo0 alias 127.0.0.2 first):
//
//	go run . -config node1.yaml
//	go run . -config node2.yaml
//	curl -si -X POST 127.0.0.2:8080/orders/v1/tenants/acme/orders -d '{"item":"book"}'
//	curl -s 127.0.0.1:8080/orders/v1/tenants/acme/orders     # node1 has it too
//	curl -s 127.0.0.1:8080/-/broadcast/<X-Infrapi-Broadcast-Id>  # how each replica took it
//
// To change the API: edit the contract, run go generate, and let the compiler
// list what the handlers must now do.
package main

import (
	"context"
	_ "embed"
	"net/http"

	"github.com/infrapi/lib/pkg/app"
	"github.com/infrapi/lib/pkg/app/problem"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/server"

	api "github.com/infrapi/lib/examples/app/internal/ordersv1"
)

//go:embed api/orders/v1.yaml
var ordersV1 []byte

// Config is the service's own section of config.yaml.
type Config struct {
	MaxQuantity int `yaml:"max_quantity"`
}

func main() {
	var cfg Config
	app.Main(app.Spec{
		Name:    "orders-api",
		Service: &cfg,
		// The ring the contract gives its writes to (x-infrapi-writes): a
		// broadcast one, every member performs every write.
		Roles: []cluster.RoleSpec{{Name: "everybody", Mode: cluster.ModeAll}},
		API: func(_ context.Context, _ *app.Runtime) ([]app.Version, error) {
			return []app.Version{{
				OpenAPI: server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
				Routes:  func(r app.Router) { r.Mount(handlerV1(newOrders(cfg.MaxQuantity))) },
			}}, nil
		},
	})
}

// handlerV1 is the generated router over the handlers, with every error it
// meets rendered as the API's one error shape.
func handlerV1(h api.StrictServerInterface) http.Handler {
	invalid := func(w http.ResponseWriter, r *http.Request, err error) {
		problem.Write(w, r, problem.Invalid("%v", err))
	}
	strict := api.NewStrictHandlerWithOptions(h, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  invalid,
		ResponseErrorHandlerFunc: problem.Write,
	})
	return api.HandlerWithOptions(strict, api.StdHTTPServerOptions{ErrorHandlerFunc: invalid})
}
