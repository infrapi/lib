package gateway

import "context"

// servicesPath is the "services" collection's URL segment under
// /apisix/admin.
const servicesPath = "services"

// NewService creates or fully replaces the service named id with service.
// service's Upstream field accepts the *Upstream returned by
// [Client.NewUpstream] or [Client.GetUpstream] directly.
func (c *Client) NewService(ctx context.Context, id string, service Service) (*Service, error) {
	var out Service
	if err := c.newItemWithID(ctx, servicesPath, id, service, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateService creates service at an id APISIX generates, returning that id
// alongside the created service.
func (c *Client) CreateService(ctx context.Context, service Service) (id string, created *Service, err error) {
	var out Service
	id, err = c.newItem(ctx, servicesPath, service, &out)
	if err != nil {
		return "", nil, err
	}
	return id, &out, nil
}

// GetService returns the service named id.
func (c *Client) GetService(ctx context.Context, id string) (*Service, error) {
	var out Service
	if err := c.getItem(ctx, servicesPath, id, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListServices returns every service in the collection.
func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	raws, err := c.listItems(ctx, servicesPath)
	if err != nil {
		return nil, err
	}
	o := origin(servicesPath, "List")
	out := make([]Service, len(raws))
	for i, raw := range raws {
		if err := decode(raw, o, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateService applies patch, a partial value, to the service named id.
// Fields patch leaves unset are kept as they are; to replace a service
// wholesale use NewService instead. patch is typically a map[string]any
// carrying only the fields to change.
func (c *Client) UpdateService(ctx context.Context, id string, patch any) (*Service, error) {
	var out Service
	if err := c.updateItem(ctx, servicesPath, id, patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteService removes the service named id.
func (c *Client) DeleteService(ctx context.Context, id string) error {
	return c.deleteItem(ctx, servicesPath, id)
}
