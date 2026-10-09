package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/infrapi/lib/pkg/app"
	"github.com/infrapi/lib/pkg/app/problem"

	api "github.com/infrapi/lib/examples/app/internal/ordersv1"
)

// orders implements the generated strict interface: one method per operation
// of the contract, typed requests in, typed responses out. The compiler
// refuses to build while an operation is missing.
//
// Errors are returned, never written: a *problem.Error becomes its status and
// problem document, anything else a 500 that hides its text. Validation of
// the request against the contract has already happened.
//
// The orders live in memory, per tenant: the example is about the wiring. A
// real service calls its backend here, or better, keeps its rules in a
// package of their own that knows nothing of the contract's version.
type orders struct {
	max int

	mu       sync.Mutex
	next     int
	byTenant map[string][]api.Order
}

var _ api.StrictServerInterface = (*orders)(nil)

func newOrders(max int) *orders {
	return &orders{max: max, byTenant: map[string][]api.Order{}}
}

// ListOrders handles GET …/orders.
func (o *orders) ListOrders(_ context.Context, req api.ListOrdersRequestObject) (api.ListOrdersResponseObject, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := api.ListOrders200JSONResponse{}
	for _, order := range o.byTenant[req.Tenant] {
		if req.Params.Item == nil || order.Item == *req.Params.Item {
			out = append(out, order)
		}
	}
	return out, nil
}

// PlaceOrder handles POST …/orders.
func (o *orders) PlaceOrder(ctx context.Context, req api.PlaceOrderRequestObject) (api.PlaceOrderResponseObject, error) {
	quantity, err := o.quantity(req.Body)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	// The id comes from the write: every replica that runs it — the contract
	// broadcasts writes — gives the order the same one. Called on its own, as
	// in a test, the write has none: a counter does.
	id := app.BroadcastID(ctx)
	if id == "" {
		o.next++
		id = fmt.Sprintf("%d", o.next)
	}
	order := api.Order{Id: "o-" + id, Item: req.Body.Item, Quantity: quantity}
	o.byTenant[req.Tenant] = append(o.byTenant[req.Tenant], order)
	return api.PlaceOrder201JSONResponse(order), nil
}

// GetOrder handles GET …/orders/{id}.
func (o *orders) GetOrder(_ context.Context, req api.GetOrderRequestObject) (api.GetOrderResponseObject, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	i, err := o.find(req.Tenant, req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetOrder200JSONResponse(o.byTenant[req.Tenant][i]), nil
}

// ReplaceOrder handles PUT …/orders/{id}.
func (o *orders) ReplaceOrder(_ context.Context, req api.ReplaceOrderRequestObject) (api.ReplaceOrderResponseObject, error) {
	quantity, err := o.quantity(req.Body)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	i, err := o.find(req.Tenant, req.Id)
	if err != nil {
		return nil, err
	}
	order := api.Order{Id: req.Id, Item: req.Body.Item, Quantity: quantity}
	o.byTenant[req.Tenant][i] = order
	return api.ReplaceOrder200JSONResponse(order), nil
}

// CancelOrder handles DELETE …/orders/{id}. An order already gone is a
// success: the caller's retry must not fail because the first attempt worked.
func (o *orders) CancelOrder(_ context.Context, req api.CancelOrderRequestObject) (api.CancelOrderResponseObject, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i, err := o.find(req.Tenant, req.Id); err == nil {
		list := o.byTenant[req.Tenant]
		o.byTenant[req.Tenant] = append(list[:i], list[i+1:]...)
	}
	return api.CancelOrder204Response{}, nil
}

// quantity applies the rule the contract cannot state: the configured
// maximum.
func (o *orders) quantity(spec *api.OrderSpec) (int, error) {
	if spec == nil {
		return 0, problem.Invalid("a body is required")
	}
	q := 1
	if spec.Quantity != nil {
		q = *spec.Quantity
	}
	if o.max > 0 && q > o.max {
		return 0, problem.Invalid("at most %d of an item per order", o.max)
	}
	return q, nil
}

// find is the order's index in the tenant's list. The caller holds o.mu.
func (o *orders) find(tenant, id string) (int, error) {
	for i, order := range o.byTenant[tenant] {
		if order.Id == id {
			return i, nil
		}
	}
	return 0, problem.NotFound("no order %q", id)
}
