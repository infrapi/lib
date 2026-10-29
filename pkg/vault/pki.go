package vault

import (
	"context"

	api "github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
)

// GetCertificate reads the certificate serial from the PKI engine mounted
// at mountPath.
//
// https://pkg.go.dev/github.com/hashicorp/vault-client-go#Secrets.PkiReadCert
func (c *Client) GetCertificate(ctx context.Context, mountPath, serial string) ([]byte, error) {
	var resp *api.Response[schema.PkiReadCertResponse]
	err := c.authenticated(ctx, func() (err error) {
		resp, err = c.APIClient.Secrets.PkiReadCert(ctx, serial, api.WithMountPath(mountPath))
		return err
	})
	if err != nil {
		return nil, classify(err, "get certificate %q", serial)
	}
	return []byte(resp.Data.Certificate), nil
}
