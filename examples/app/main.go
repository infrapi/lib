// Command app is the template of an InfraPI service on pkg/app: a contract,
// its handlers, and one call. Everything else — logs, metrics, docs,
// validation, errors, idempotency, cluster, gateway, shutdown — is the
// library's, configured from config.yaml and the environment.
//
//	cd examples/app
//	go run . -config config.yaml
//	curl -s -X POST localhost:8080/orders/v1/tenants/acme/orders -d '{"item":"book"}'
//	open http://localhost:8080/-/docs
package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"sync"

	"github.com/infrapi/lib/pkg/app"
	"github.com/infrapi/lib/pkg/app/problem"
	"github.com/infrapi/lib/pkg/server"
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
		Name:         "orders-api",
		Service:      &cfg,
		LeaderWrites: true,
		API: func(_ context.Context, _ *app.Runtime) ([]app.Version, error) {
			h := &orders{max: cfg.MaxQuantity, byID: map[string]order{}}
			return []app.Version{{
				OpenAPI: server.OpenAPISpec{Name: "orders v1", Path: "orders/v1.yaml", Content: ordersV1},
				Routes: func(r app.Router) {
					r.POST("/orders/v1/tenants/{tenant}/orders", h.place)
					r.GET("/orders/v1/tenants/{tenant}/orders/{id}", h.read)
				},
			}}, nil
		},
	})
}

type order struct {
	ID       string `json:"id"`
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

// orders keeps them in memory: the example is about the wiring, not storage.
type orders struct {
	mu   sync.Mutex
	max  int
	byID map[string]order
}

func (o *orders) place(c *app.Context) {
	var in struct {
		Item     string `json:"item"`
		Quantity int    `json:"quantity"`
	}
	if err := c.Bind(&in); err != nil {
		c.Error(err)
		return
	}
	if in.Quantity == 0 {
		in.Quantity = 1
	}
	if o.max > 0 && in.Quantity > o.max {
		c.Error(problem.Invalid("at most %d of an item per order", o.max))
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	id := fmt.Sprintf("%s-%d", c.Param("tenant"), len(o.byID)+1)
	o.byID[id] = order{ID: id, Item: in.Item, Quantity: in.Quantity}
	c.JSON(http.StatusCreated, o.byID[id])
}

func (o *orders) read(c *app.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	got, ok := o.byID[c.Param("id")]
	if !ok {
		c.Error(problem.NotFound("no order %q", c.Param("id")))
		return
	}
	c.JSON(http.StatusOK, got)
}
