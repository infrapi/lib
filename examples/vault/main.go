package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	infravault "github.com/infrapi/lib/pkg/vault"
)

func main() {
	fmt.Println("=== InfraPi Vault Package Examples ===")
	fmt.Println()

	srv := startVaultStub()
	defer srv.Close()

	client, err := infravault.NewClient(infravault.ClientOptions{Address: srv.URL})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}

	// Example 1: authenticate, then let every later call carry the token.
	example1(client)

	// Example 2: read, write, list and delete a KV v2 secret.
	example2(client)

	// Example 3: read a certificate from the PKI engine.
	example3(client)
}

// Example 1: Login with a token issued out of band.
func example1(client *infravault.Client) {
	fmt.Println("--- Example 1: Login ---")

	if err := client.Login(context.Background(), infravault.LoginOptions{Token: "s.example-token"}); err != nil {
		log.Fatalf("Login: %v", err)
	}
	fmt.Println("authenticated")
	fmt.Println()
}

// Example 2: GetSecret, SetSecret, ListSecrets, DeleteSecret.
func example2(client *infravault.Client) {
	fmt.Println("--- Example 2: Secrets (KV v2) ---")
	ctx := context.Background()

	data, err := client.GetSecret(ctx, "secret", "myapp/db")
	if err != nil {
		log.Fatalf("GetSecret: %v", err)
	}
	fmt.Printf("GetSecret:    %v\n", data)

	data, err = client.SetSecret(ctx, "secret", "myapp/db", map[string]any{"password": "rotated"})
	if err != nil {
		log.Fatalf("SetSecret: %v", err)
	}
	fmt.Printf("SetSecret:    %v\n", data)

	keys, err := client.ListSecrets(ctx, "secret", "myapp")
	if err != nil {
		log.Fatalf("ListSecrets: %v", err)
	}
	fmt.Printf("ListSecrets:  %v\n", keys)

	if err := client.DeleteSecret(ctx, "secret", "myapp/db"); err != nil {
		log.Fatalf("DeleteSecret: %v", err)
	}
	fmt.Println("DeleteSecret: ok")
	fmt.Println()
}

// Example 3: GetCertificate.
func example3(client *infravault.Client) {
	fmt.Println("--- Example 3: Certificates (PKI) ---")

	cert, err := client.GetCertificate(context.Background(), "pki", "12:34:56")
	if err != nil {
		log.Fatalf("GetCertificate: %v", err)
	}
	fmt.Printf("GetCertificate: %s\n", cert)
}

// startVaultStub stands in for a real Vault server: enough of the KV v2
// and PKI secrets engines' HTTP API to run the examples above against.
//
// It dispatches on r.URL.Path itself rather than http.ServeMux: a KV v2
// path such as "myapp/db" reaches the wire %2F-escaped as a single
// segment, and Go 1.22+'s ServeMux matches patterns segment by segment -
// so a literal "/v1/secret/data/myapp/db" pattern never matches it.
func startVaultStub() *httptest.Server {
	data := map[string]any{"password": "s3cr3t"}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1/secret/data/myapp/db":
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{"data": data, "metadata": map[string]any{"version": 1}},
				})
			case http.MethodPost:
				var body struct {
					Data map[string]any `json:"data"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				data = body.Data
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 2}})
			case http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			}
		case "/v1/secret/metadata/myapp/":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": []string{"db"}}})
		case "/v1/pki/cert/12:34:56":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"certificate": "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":["not found"]}`))
		}
	}))
}
