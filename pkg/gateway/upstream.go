package gateway

import "context"

// upstreamsPath is the "upstreams" collection's URL segment under
// /apisix/admin.
const upstreamsPath = "upstreams"

// NewUpstream creates or fully replaces the upstream named id with upstream.
func (c *Client) NewUpstream(ctx context.Context, id string, upstream Upstream) (*Upstream, error) {
	var out Upstream
	if err := c.newItemWithID(ctx, upstreamsPath, id, upstream, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateUpstream creates upstream at an id APISIX generates, returning that
// id alongside the created upstream.
func (c *Client) CreateUpstream(ctx context.Context, upstream Upstream) (id string, created *Upstream, err error) {
	var out Upstream
	id, err = c.newItem(ctx, upstreamsPath, upstream, &out)
	if err != nil {
		return "", nil, err
	}
	return id, &out, nil
}

// GetUpstream returns the upstream named id.
func (c *Client) GetUpstream(ctx context.Context, id string) (*Upstream, error) {
	var out Upstream
	if err := c.getItem(ctx, upstreamsPath, id, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListUpstreams returns every upstream in the collection.
func (c *Client) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	raws, err := c.listItems(ctx, upstreamsPath)
	if err != nil {
		return nil, err
	}
	o := origin(upstreamsPath, "List")
	out := make([]Upstream, len(raws))
	for i, raw := range raws {
		if err := decode(raw, o, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateUpstream applies patch, a partial value, to the upstream named id.
// Fields patch leaves unset are kept as they are; to replace an upstream
// wholesale use NewUpstream instead. patch is typically a map[string]any
// carrying only the fields to change.
func (c *Client) UpdateUpstream(ctx context.Context, id string, patch any) (*Upstream, error) {
	var out Upstream
	if err := c.updateItem(ctx, upstreamsPath, id, patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteUpstream removes the upstream named id.
func (c *Client) DeleteUpstream(ctx context.Context, id string) error {
	return c.deleteItem(ctx, upstreamsPath, id)
}
