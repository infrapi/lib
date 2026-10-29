package vault

import (
	"context"

	api "github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
)

// GetSecret reads path from the KV v2 engine mounted at mountPath.
//
// https://pkg.go.dev/github.com/hashicorp/vault-client-go#Secrets.KvV2Read
func (c *Client) GetSecret(ctx context.Context, mountPath, path string) (map[string]any, error) {
	var resp *api.Response[schema.KvV2ReadResponse]
	err := c.authenticated(ctx, func() (err error) {
		resp, err = c.APIClient.Secrets.KvV2Read(ctx, path, api.WithMountPath(mountPath))
		return err
	})
	if err != nil {
		return nil, classify(err, "get secret %q", path)
	}
	return resp.Data.Data, nil
}

// SetSecret writes data to path in the KV v2 engine mounted at mountPath,
// creating a new version, and returns what Vault now stores there.
//
// https://pkg.go.dev/github.com/hashicorp/vault-client-go#Secrets.KvV2Write
func (c *Client) SetSecret(ctx context.Context, mountPath, path string, data map[string]any) (map[string]any, error) {
	err := c.authenticated(ctx, func() error {
		_, err := c.APIClient.Secrets.KvV2Write(ctx, path, schema.KvV2WriteRequest{Data: data},
			api.WithMountPath(mountPath))
		return err
	})
	if err != nil {
		return nil, classify(err, "set secret %q", path)
	}

	// KvV2Write's own response carries only version metadata (created
	// time, version number, ...), never the data just written, so read
	// it back to hand the caller what Vault now stores.
	return c.GetSecret(ctx, mountPath, path)
}

// DeleteSecret soft-deletes the current version of path in the KV v2
// engine mounted at mountPath - recoverable with Vault's own undelete
// until its configured versions are pruned.
//
// https://pkg.go.dev/github.com/hashicorp/vault-client-go#Secrets.KvV2Delete
func (c *Client) DeleteSecret(ctx context.Context, mountPath, path string) error {
	err := c.authenticated(ctx, func() error {
		_, err := c.APIClient.Secrets.KvV2Delete(ctx, path, api.WithMountPath(mountPath))
		return err
	})
	if err != nil {
		return classify(err, "delete secret %q", path)
	}
	return nil
}

// ListSecrets lists the secrets and folders directly under path in the KV
// v2 engine mounted at mountPath.
//
// https://pkg.go.dev/github.com/hashicorp/vault-client-go#Secrets.KvV2List
func (c *Client) ListSecrets(ctx context.Context, mountPath, path string) ([]string, error) {
	var resp *api.Response[schema.StandardListResponse]
	err := c.authenticated(ctx, func() (err error) {
		resp, err = c.APIClient.Secrets.KvV2List(ctx, path, api.WithMountPath(mountPath))
		return err
	})
	if err != nil {
		return nil, classify(err, "list secrets %q", path)
	}
	return resp.Data.Keys, nil
}
