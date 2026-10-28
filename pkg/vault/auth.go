package vault

import (
	"context"
	"fmt"

	api "github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
)

// DefaultAppRoleMountPath is used when AppRoleLogin.MountPath is empty,
// matching Vault's own default mount point for the AppRole auth method.
const DefaultAppRoleMountPath = "approle"

// AppRoleLogin authenticates via the AppRole auth method
// (https://developer.hashicorp.com/vault/docs/auth/approle). RoleID and
// SecretID are mandatory.
type AppRoleLogin struct {
	RoleID   string
	SecretID string
	// MountPath the AppRole auth method is enabled on (default:
	// DefaultAppRoleMountPath).
	MountPath string
}

// LoginOptions selects how Login authenticates the client. Exactly one of
// AppRole or Token must be set.
type LoginOptions struct {
	// AppRole authenticates via the AppRole auth method.
	AppRole *AppRoleLogin

	// Token sets the client's token directly, for one already issued out
	// of band (vault login, a token minted by another service, ...),
	// without a login call of its own.
	Token string
}

// Login authenticates the client per opts, so every subsequent call this
// Client makes carries the resulting token.
func (c *Client) Login(ctx context.Context, opts LoginOptions) error {
	switch {
	case opts.AppRole != nil:
		return c.loginAppRole(ctx, *opts.AppRole)
	case opts.Token != "":
		if err := c.APIClient.SetToken(opts.Token); err != nil {
			return fmt.Errorf("vault: set token: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("vault: Login requires AppRole or Token")
	}
}

func (c *Client) loginAppRole(ctx context.Context, opts AppRoleLogin) error {
	if opts.RoleID == "" || opts.SecretID == "" {
		return fmt.Errorf("vault: AppRole login requires RoleID and SecretID")
	}

	mountPath := opts.MountPath
	if mountPath == "" {
		mountPath = DefaultAppRoleMountPath
	}

	resp, err := c.APIClient.Auth.AppRoleLogin(
		ctx,
		schema.AppRoleLoginRequest{RoleId: opts.RoleID, SecretId: opts.SecretID},
		api.WithMountPath(mountPath),
	)
	if err != nil {
		return fmt.Errorf("vault: AppRole login: %w", err)
	}
	if resp.Auth == nil {
		return fmt.Errorf("vault: AppRole login: response carried no auth block")
	}

	if err := c.APIClient.SetToken(resp.Auth.ClientToken); err != nil {
		return fmt.Errorf("vault: set token: %w", err)
	}
	return nil
}
