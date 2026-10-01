package gateway

import (
	"context"
	"encoding/json"
	"strings"

	infraerrors "github.com/infrapi/lib/pkg/errors"
	infrahttp "github.com/infrapi/lib/pkg/http"
)

// errorEnvelope is the Admin API's response shape for a failed call.
type errorEnvelope struct {
	Message string `json:"error_msg"`
}

// itemEnvelope is the Admin API's response shape for a single resource: the
// value the caller sent or received, plus the etcd bookkeeping APISIX returns
// alongside it. Every New*, Get* and Update* function unwraps one of these
// into the concrete type it returns.
type itemEnvelope struct {
	Key           string          `json:"key,omitempty"`
	Value         json.RawMessage `json:"value"`
	CreatedIndex  int64           `json:"createdIndex,omitempty"`
	ModifiedIndex int64           `json:"modifiedIndex,omitempty"`
}

// listEnvelope is the Admin API's response shape for a collection GET.
type listEnvelope struct {
	List  []itemEnvelope `json:"list"`
	Total int            `json:"total"`
}

// url builds the Admin API URL for id within collection, or the collection
// itself when id is empty.
func (c *Client) url(collection, id string) string {
	url := c.baseURL() + "/" + collection
	if id != "" {
		url += "/" + id
	}
	return url
}

// origin names where a call broke, in the form pkg/errors.Error.Origin expects.
func origin(collection, op string) string {
	return "gateway." + collection + "." + op
}

// do executes call against url, decoding a JSON response body into env when
// the call succeeds. A transport failure or a non-2xx response is returned as
// an [infraerrors.Error], origin naming the call that broke.
func do(call func(url string) (*infrahttp.Response, error), url, origin string, env *itemEnvelope) error {
	resp, err := call(url)
	if err != nil {
		return infraerrors.Wrap(err, infrahttp.StatusBadGateway, origin, "")
	}

	if resp.Code < 200 || resp.Code >= 300 {
		var errEnv errorEnvelope
		_ = json.Unmarshal(resp.Body, &errEnv)

		message := errEnv.Message
		if message == "" {
			message = infrahttp.StatusText(resp.Code)
		}

		return infraerrors.New(resp.Code, origin, "%s", message)
	}

	if env == nil || len(resp.Body) == 0 {
		return nil
	}

	if err := json.Unmarshal(resp.Body, env); err != nil {
		return infraerrors.Wrap(err, infrahttp.StatusBadGateway, origin, "decode Admin API response")
	}

	return nil
}

// decode unmarshals raw - an itemEnvelope.Value - into out, wrapping a
// failure as an [infraerrors.Error] the same way do does.
func decode(raw json.RawMessage, origin string, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return infraerrors.Wrap(err, infrahttp.StatusBadGateway, origin, "decode Admin API response")
	}
	return nil
}

// newItem creates value at a generated id in collection, decoding the result
// into out and returning that id.
func (c *Client) newItem(ctx context.Context, collection string, value, out any) (id string, err error) {
	var env itemEnvelope
	o := origin(collection, "Create")
	req := c.HTTP.NewRequest(ctx).SetBody(value)
	if err := do(req.Post, c.url(collection, ""), o, &env); err != nil {
		return "", err
	}
	if err := decode(env.Value, o, out); err != nil {
		return "", err
	}
	return idFromKey(env.Key), nil
}

// idFromKey extracts the bare id from an etcd key such as
// "/apisix/routes/auto-1", so it can be fed straight back into Get/Update/
// Delete<Type>, which build their own URL from collection and id.
func idFromKey(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// newItemWithID creates or fully replaces the item named id in collection
// with value, decoding the result into out.
func (c *Client) newItemWithID(ctx context.Context, collection, id string, value, out any) error {
	var env itemEnvelope
	o := origin(collection, "New")
	req := c.HTTP.NewRequest(ctx).SetBody(value)
	if err := do(req.Put, c.url(collection, id), o, &env); err != nil {
		return err
	}
	return decode(env.Value, o, out)
}

// getItem decodes the item named id in collection into out.
func (c *Client) getItem(ctx context.Context, collection, id string, out any) error {
	var env itemEnvelope
	o := origin(collection, "Get")
	req := c.HTTP.NewRequest(ctx)
	if err := do(req.Get, c.url(collection, id), o, &env); err != nil {
		return err
	}
	return decode(env.Value, o, out)
}

// listItems returns the raw value of every item in collection, for the
// caller to decode into its concrete element type.
func (c *Client) listItems(ctx context.Context, collection string) ([]json.RawMessage, error) {
	o := origin(collection, "List")
	req := c.HTTP.NewRequest(ctx)
	resp, err := req.Get(c.url(collection, ""))
	if err != nil {
		return nil, infraerrors.Wrap(err, infrahttp.StatusBadGateway, o, "")
	}
	if resp.Code < 200 || resp.Code >= 300 {
		var errEnv errorEnvelope
		_ = json.Unmarshal(resp.Body, &errEnv)
		message := errEnv.Message
		if message == "" {
			message = infrahttp.StatusText(resp.Code)
		}
		return nil, infraerrors.New(resp.Code, o, "%s", message)
	}

	var env listEnvelope
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, infraerrors.Wrap(err, infrahttp.StatusBadGateway, o, "decode Admin API response")
	}

	values := make([]json.RawMessage, len(env.List))
	for i, item := range env.List {
		values[i] = item.Value
	}
	return values, nil
}

// updateItem applies patch, a partial value, to the item named id in
// collection, decoding the result into out.
func (c *Client) updateItem(ctx context.Context, collection, id string, patch, out any) error {
	var env itemEnvelope
	o := origin(collection, "Update")
	req := c.HTTP.NewRequest(ctx).SetBody(patch)
	if err := do(req.Patch, c.url(collection, id), o, &env); err != nil {
		return err
	}
	return decode(env.Value, o, out)
}

// deleteItem removes the item named id in collection.
func (c *Client) deleteItem(ctx context.Context, collection, id string) error {
	req := c.HTTP.NewRequest(ctx)
	return do(req.Delete, c.url(collection, id), origin(collection, "Delete"), nil)
}
