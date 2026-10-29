package vault

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	api "github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// DefaultAppRoleMountPath is used when AppRoleLogin.MountPath is empty,
// matching Vault's own default mount point for the AppRole auth method.
const DefaultAppRoleMountPath = "approle"

// AppRoleLogin authenticates via the AppRole auth method
// (https://developer.hashicorp.com/vault/docs/auth/approle). RoleID is
// mandatory, and SecretID or SecretIDFile.
type AppRoleLogin struct {
	RoleID   string
	SecretID string
	// SecretIDFile is a file holding the SecretID, read at each login, so
	// that a SecretID the platform rotates is picked up. SecretID wins when
	// both are set.
	SecretIDFile string
	// MountPath the AppRole auth method is enabled on (default:
	// DefaultAppRoleMountPath).
	MountPath string
}

// LoginOptions selects how Login authenticates the client. Exactly one of
// AppRole or Token must be set.
type LoginOptions struct {
	// AppRole authenticates via the AppRole auth method. The client logs in
	// again on its own when the token is about to expire, or when Vault
	// refuses it.
	AppRole *AppRoleLogin

	// Token sets the client's token directly, for one already issued out
	// of band (vault login, a token minted by another service, ...),
	// without a login call of its own.
	Token string
}

// Login authenticates the client per opts, so every subsequent call this
// Client makes carries the resulting token.
func (c *Client) Login(ctx context.Context, opts LoginOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case opts.AppRole != nil:
		if opts.AppRole.RoleID == "" || (opts.AppRole.SecretID == "" && opts.AppRole.SecretIDFile == "") {
			return fmt.Errorf("vault: AppRole login requires RoleID and SecretID or SecretIDFile")
		}
		login := *opts.AppRole
		if err := c.loginAppRole(ctx, login); err != nil {
			return err
		}
		c.appRole = &login
		return nil
	case opts.Token != "":
		if err := c.APIClient.SetToken(opts.Token); err != nil {
			return fmt.Errorf("vault: set token: %w", err)
		}
		c.appRole, c.token, c.expires = nil, opts.Token, time.Time{}
		return nil
	default:
		return fmt.Errorf("vault: Login requires AppRole or Token")
	}
}

// loginAppRole logs in and sets the token it gets; c.mu is held.
func (c *Client) loginAppRole(ctx context.Context, opts AppRoleLogin) error {
	secretID := opts.SecretID
	if secretID == "" {
		b, err := os.ReadFile(opts.SecretIDFile)
		if err != nil {
			return fmt.Errorf("vault: AppRole login: read SecretIDFile: %w", err)
		}
		secretID = strings.TrimSpace(string(b))
	}

	mountPath := opts.MountPath
	if mountPath == "" {
		mountPath = DefaultAppRoleMountPath
	}

	resp, err := c.APIClient.Auth.AppRoleLogin(
		ctx,
		schema.AppRoleLoginRequest{RoleId: opts.RoleID, SecretId: secretID},
		api.WithMountPath(mountPath),
	)
	if err != nil {
		var re *api.ResponseError
		if errors.As(err, &re) && re.StatusCode < http.StatusInternalServerError {
			e := infraerrors.UpstreamAuth("vault: AppRole login refused: status %d", re.StatusCode)
			e.Err = err
			return e
		}
		return classify(err, "AppRole login")
	}
	if resp.Auth == nil {
		return fmt.Errorf("vault: AppRole login: response carried no auth block")
	}

	if err := c.APIClient.SetToken(resp.Auth.ClientToken); err != nil {
		return fmt.Errorf("vault: set token: %w", err)
	}
	c.token, c.expires = resp.Auth.ClientToken, time.Time{}
	if lease := time.Duration(resp.Auth.LeaseDuration) * time.Second; lease > 0 {
		// Logged in again a little before the end, rather than renewed:
		// a renewed token still dies at its max TTL.
		c.expires = c.now().Add(lease * 9 / 10)
	}
	return nil
}

// authenticated runs call with a valid token: after logging in again when
// the AppRole token is about to expire, and once more after logging in again
// when Vault refuses it, revoked or expired before its time. With a token
// set by Login, call runs once, as it is.
func (c *Client) authenticated(ctx context.Context, call func() error) error {
	c.mu.Lock()
	if c.appRole != nil && !c.expires.IsZero() && !c.now().Before(c.expires) {
		if err := c.loginAppRole(ctx, *c.appRole); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	used := c.token
	c.mu.Unlock()

	err := call()
	if !api.IsErrorStatus(err, http.StatusForbidden) {
		return err
	}
	c.mu.Lock()
	switch {
	case c.appRole == nil:
		c.mu.Unlock()
		return err
	// Another call logged in again meanwhile: its token is the new one.
	case c.token == used:
		if err := c.loginAppRole(ctx, *c.appRole); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	c.mu.Unlock()
	return call()
}
