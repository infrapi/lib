package vault

import (
	"context"
	"fmt"

	api "github.com/hashicorp/vault-client-go"
)

// GetCertificate reads the certificate serial from the PKI engine mounted
// at mountPath.
//
// https://pkg.go.dev/github.com/hashicorp/vault-client-go#Secrets.PkiReadCert
func (c *Client) GetCertificate(ctx context.Context, mountPath, serial string) ([]byte, error) {
	resp, err := c.APIClient.Secrets.PkiReadCert(ctx, serial, api.WithMountPath(mountPath))
	if err != nil {
		return nil, fmt.Errorf("vault: get certificate %q: %w", serial, err)
	}
	return []byte(resp.Data.Certificate), nil
}
