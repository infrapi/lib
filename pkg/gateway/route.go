package gateway

import "context"

// routesPath is the "routes" collection's URL segment under /apisix/admin.
const routesPath = "routes"

// NewRoute creates or fully replaces the route named id with route. route's
// Upstream field accepts the *Upstream returned by [Client.NewUpstream] or
// [Client.GetUpstream] directly.
func (c *Client) NewRoute(ctx context.Context, id string, route Route) (*Route, error) {
	var out Route
	if err := c.newItemWithID(ctx, routesPath, id, route, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateRoute creates route at an id APISIX generates, returning that id
// alongside the created route.
func (c *Client) CreateRoute(ctx context.Context, route Route) (id string, created *Route, err error) {
	var out Route
	id, err = c.newItem(ctx, routesPath, route, &out)
	if err != nil {
		return "", nil, err
	}
	return id, &out, nil
}

// GetRoute returns the route named id.
func (c *Client) GetRoute(ctx context.Context, id string) (*Route, error) {
	var out Route
	if err := c.getItem(ctx, routesPath, id, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRoutes returns every route in the collection.
func (c *Client) ListRoutes(ctx context.Context) ([]Route, error) {
	raws, err := c.listItems(ctx, routesPath)
	if err != nil {
		return nil, err
	}
	o := origin(routesPath, "List")
	out := make([]Route, len(raws))
	for i, raw := range raws {
		if err := decode(raw, o, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateRoute applies patch, a partial value, to the route named id. Fields
// patch leaves unset are kept as they are; to replace a route wholesale use
// NewRoute instead. patch is typically a map[string]any carrying only the
// fields to change.
func (c *Client) UpdateRoute(ctx context.Context, id string, patch any) (*Route, error) {
	var out Route
	if err := c.updateItem(ctx, routesPath, id, patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRoute removes the route named id.
func (c *Client) DeleteRoute(ctx context.Context, id string) error {
	return c.deleteItem(ctx, routesPath, id)
}
