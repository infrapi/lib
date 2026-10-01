package gateway

import "context"

// consumersPath is the "consumers" collection's URL segment under
// /apisix/admin.
const consumersPath = "consumers"

// NewConsumer creates or fully replaces the consumer named consumer.Username.
// Consumers are keyed by username rather than a generated id, so there is no
// auto-id CreateConsumer.
func (c *Client) NewConsumer(ctx context.Context, consumer Consumer) (*Consumer, error) {
	var out Consumer
	if err := c.newItemWithID(ctx, consumersPath, consumer.Username, consumer, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetConsumer returns the consumer named username.
func (c *Client) GetConsumer(ctx context.Context, username string) (*Consumer, error) {
	var out Consumer
	if err := c.getItem(ctx, consumersPath, username, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListConsumers returns every consumer in the collection.
func (c *Client) ListConsumers(ctx context.Context) ([]Consumer, error) {
	raws, err := c.listItems(ctx, consumersPath)
	if err != nil {
		return nil, err
	}
	o := origin(consumersPath, "List")
	out := make([]Consumer, len(raws))
	for i, raw := range raws {
		if err := decode(raw, o, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateConsumer applies patch, a partial value, to the consumer named
// username. Fields patch leaves unset are kept as they are; to replace a
// consumer wholesale use NewConsumer instead. patch is typically a
// map[string]any carrying only the fields to change.
func (c *Client) UpdateConsumer(ctx context.Context, username string, patch any) (*Consumer, error) {
	var out Consumer
	if err := c.updateItem(ctx, consumersPath, username, patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteConsumer removes the consumer named username.
func (c *Client) DeleteConsumer(ctx context.Context, username string) error {
	return c.deleteItem(ctx, consumersPath, username)
}
